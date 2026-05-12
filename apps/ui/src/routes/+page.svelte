<script lang="ts">
  import { goto } from '$app/navigation';
  import { resolve } from '$app/paths';
  import { page } from '$app/state';
  import { env } from '$env/dynamic/public';
  import * as api from '$lib/api';
  import bankLogo from '$lib/assets/logo.svg';
  import ActionsPanel from '$lib/components/ActionsPanel.svelte';
  import ApplicationSummary from '$lib/components/ApplicationSummary.svelte';
  import AuditTimeline from '$lib/components/AuditTimeline.svelte';
  import { applicationExecutionKey } from '$lib/types';
  import type {
    ApplicationListItem,
    ApplicationWorkflowStatus,
    MortgageApplication,
    ScenarioOption,
  } from '$lib/types';
  import {
    workflowStatusLabel,
    workflowVersionLabel,
    workflowVersionStyle,
  } from '$lib/utils';
  import { untrack } from 'svelte';

  import type { PageData } from './$types';

  let { data }: { data: PageData } = $props();

  const bankName = env.PUBLIC_BANK_NAME ?? 'Temporal Bank';
  const refreshTimeout = 3000;

  // ── Scenarios ───────────────────────────────────────────────────────────
  const DEFAULT_SCENARIOS: ScenarioOption[] = [
    { name: 'happy_path', description: 'Full successful mortgage workflow.' },
    {
      name: 'fail_after_offer_reservation',
      description: 'Fails after the offer reservation step.',
    },
    {
      name: 'fail_and_compensate_after_offer_reservation',
      description: 'Fails and compensates after the offer reservation step.',
    },
  ];

  // untrack: intentional one-time snapshot from load data; polling/actions manage state locally.
  let scenarios: ScenarioOption[] = $state(untrack(() => data.scenarios));

  // ── Applications list ────────────────────────────────────────────────────
  let applications: ApplicationListItem[] = $state(
    untrack(() => data.applications),
  );

  function workflowStatusStyle(status: ApplicationWorkflowStatus): string {
    const map: Record<ApplicationWorkflowStatus, string> = {
      running: 'background:#dbeafe;color:#1e40af;border-color:#93c5fd',
      completed: 'background:#f0fdf4;color:#15803d;border-color:#bbf7d0',
      failed: 'background:#fef2f2;color:#b91c1c;border-color:#fecaca',
      cancelled: 'background:#f3f4f6;color:#374151;border-color:#d1d5db',
      terminated: 'background:#fce7f3;color:#9d174d;border-color:#f9a8d4',
      timed_out: 'background:#fffbeb;color:#92400e;border-color:#fde68a',
      continued_as_new: 'background:#dbeafe;color:#1e40af;border-color:#93c5fd',
      unknown: 'background:#f3f4f6;color:#374151;border-color:#d1d5db',
    };
    return map[status];
  }

  const defaultName = 'Patrick Clifton';

  // ── Start form ──────────────────────────────────────────────────────────
  let startId = $state(crypto.randomUUID());
  let startName = $state(defaultName);
  let startScenario = $state('happy_path');
  let startFailureRate = $state(0);
  let startError = $state('');
  let startLoading = $state(false);

  // ── Load state ──────────────────────────────────────────────────────────
  let loadError = $state('');
  let loadLoading = $state(false);

  // ── Current application ─────────────────────────────────────────────────
  let app = $state<MortgageApplication | null>(untrack(() => data.app));
  let refreshing = $state(false);
  let refreshError = $state('');

  // ── Derived ─────────────────────────────────────────────────────────────
  const TERMINAL = new Set(['completed', 'rejected', 'compensated']);

  function allowsFailureInjection(scenario: string): boolean {
    return scenario === 'happy_path';
  }

  const scenarioOptions = $derived(
    scenarios.length > 0 ? scenarios : DEFAULT_SCENARIOS,
  );
  const selectedDesc = $derived(
    scenarioOptions.find((s) => s.name === startScenario)?.description ?? '',
  );
  const showFailureSlider = $derived(allowsFailureInjection(startScenario));
  // Action visibility is gated on the precise pending dependency reported by
  // the workflow rather than a broad business-status flag, so the form only
  // appears while the workflow is genuinely waiting for that specific
  // operator input.
  const isCreditCheckPending = $derived(
    app?.workflowStatus === 'running' &&
      app?.pendingDependency === 'credit_check',
  );
  // v2 workflow is waiting for the operator to submit a property value. This
  // is the gate that drives the "Submit Property Valuation" UI action and is
  // only ever true when the worker is the v2 build with no value submitted
  // yet.
  const isPropertyValuationPending = $derived(
    app?.workflowStatus === 'running' &&
      app?.workflowVersion === 'v2' &&
      app?.pendingDependency === 'property_valuation',
  );
  const isTerminal = $derived(app ? TERMINAL.has(app.status) : false);
  // Polling tracks the workflow's lifecycle state, NOT the business state.
  // The two diverge: the workflow can have business `status === 'completed'`
  // for several seconds while it is still running its tail activities (e.g.
  // SendNotification, with its random delay and retry policy). Stopping
  // polling on business completion would freeze the UI before the final
  // workflowStatus and audit entries arrive. Polling therefore continues
  // while `workflowStatus === 'running'` and only stops once a non-running
  // status has been observed and rendered. Undefined is treated as running
  // so an in-flight initial load never permanently freezes the UI.
  const isWorkflowRunning = $derived(
    !app?.workflowStatus || app.workflowStatus === 'running',
  );

  // Reset failure rate when switching away from happy_path so the slider
  // always starts at 0 when the user returns to happy_path.
  $effect(() => {
    if (!allowsFailureInjection(startScenario)) {
      startFailureRate = 0;
    }
  });

  // ── Sync from load data on navigation ───────────────────────────────────
  // The untrack snapshots above are initialised once. This effect re-syncs
  // them whenever SvelteKit re-runs load (e.g. navigating home clears app).
  $effect(() => {
    app = data.app ?? null;
    applications = data.applications;
    loadError = '';
  });

  // ── Polling via $effect ──────────────────────────────────────────────────
  $effect(() => {
    if (!app || !isWorkflowRunning) return;
    const timer = setInterval(() => void doRefresh(), refreshTimeout);
    return () => clearInterval(timer);
  });

  // ── Core actions ─────────────────────────────────────────────────────────
  async function refreshApplications() {
    try {
      applications = await api.getApplications();
    } catch {
      // fail softly — list will be stale but the create flow still succeeds
    }
  }

  async function doRefresh() {
    if (!app) return;
    refreshing = true;
    refreshError = '';
    try {
      app = await api.getApplication(app.applicationId, app.runId);
    } catch (e) {
      refreshError = e instanceof Error ? e.message : 'Refresh failed';
    } finally {
      refreshing = false;
    }
    await refreshApplications();
  }

  async function loadApplication(id: string, runId?: string) {
    loadLoading = true;
    loadError = '';
    try {
      app = await api.getApplication(id, runId);

      const url = new URL(resolve('/'), page.url.origin);
      url.searchParams.set('applicationId', id);
      if (runId) {
        url.searchParams.set('runId', runId);
      } else {
        url.searchParams.delete('runId');
      }

      // eslint-disable-next-line svelte/no-navigation-without-resolve
      await goto(url, {
        replaceState: true,
        keepFocus: true,
        noScroll: true,
      });
    } catch (e) {
      loadError = e instanceof Error ? e.message : 'Failed to load application';
      app = null;
    } finally {
      loadLoading = false;
    }
  }

  // ── Form handlers ─────────────────────────────────────────────────────────
  async function handleRerun(newApplicationId: string) {
    await loadApplication(newApplicationId);
    await refreshApplications();
  }

  async function handleStart(e: SubmitEvent) {
    e.preventDefault();
    startLoading = true;
    startError = '';
    try {
      await api.startApplication({
        applicationId: startId,
        applicantName: startName.trim(),
        scenario: startScenario,
        externalFailureRatePercent: allowsFailureInjection(startScenario)
          ? startFailureRate
          : 0,
      });
      const launched = startId;
      startId = crypto.randomUUID();
      startName = defaultName;
      await loadApplication(launched);
      await refreshApplications();
    } catch (e) {
      startError =
        e instanceof Error ? e.message : 'Failed to start application';
    } finally {
      startLoading = false;
    }
  }
</script>

<svelte:head>
  <title>Mortgage Application Console</title>
</svelte:head>

<div class="console">
  <header>
    <div class="header-inner">
      <a href={resolve('/')}>
        <img src={bankLogo} alt={bankName} class="logo" /></a
      >
      <div class="title-block">
        <h1><a href={resolve('/')}>{bankName}</a></h1>
        <p class="subtitle">Mortgage Application Console</p>
      </div>
    </div>
  </header>

  <!-- ── Start application ─────────────────────────────────────────────── -->
  <section class="card">
    <h2>Start Application</h2>
    <form onsubmit={handleStart} class="start-form">
      <!-- Bank application fields -->
      <div class="form-bank">
        <div class="field">
          <label for="start-id">Application ID</label>
          <div class="input-row">
            <input
              id="start-id"
              type="text"
              bind:value={startId}
              placeholder="UUID"
              required
              class="mono"
            />
            <button
              type="button"
              class="btn-secondary"
              onclick={() => (startId = crypto.randomUUID())}>New</button
            >
          </div>
        </div>

        <div class="field">
          <label for="start-name">Applicant Name</label>
          <input id="start-name" type="text" bind:value={startName} required />
        </div>
      </div>

      <!-- Temporal demo controls -->
      <div class="demo-controls">
        <p class="demo-controls-title">Temporal Demo Controls</p>
        <p class="demo-controls-hint">
          These controls simulate demo conditions such as workflow scenarios and
          unreliable external dependencies. They are not bank operator controls.
        </p>
        <div class="demo-fields">
          <div class="field">
            <label for="start-scenario">Scenario</label>
            <select id="start-scenario" bind:value={startScenario}>
              {#each scenarioOptions as s (s.name)}
                <option value={s.name}>{s.name}</option>
              {/each}
            </select>
            {#if selectedDesc}
              <p class="hint">{selectedDesc}</p>
            {/if}
          </div>

          {#if showFailureSlider}
            <div class="field">
              <label for="start-failure-rate"
                >External failure rate <span class="rate-value"
                  >{startFailureRate}%</span
                ></label
              >
              <input
                id="start-failure-rate"
                type="range"
                min="0"
                max="75"
                step="5"
                bind:value={startFailureRate}
              />
              <p class="hint">
                Simulates flaky external systems by randomly failing eligible
                activities. Temporal retries should absorb transient failures.
              </p>
            </div>
          {/if}
        </div>
      </div>

      {#if startError}
        <p class="error">{startError}</p>
      {/if}
      <button type="submit" class="btn-primary" disabled={startLoading}>
        {startLoading ? 'Starting…' : 'Start Workflow'}
      </button>
    </form>
  </section>

  <!-- ── Existing applications ────────────────────────────────────────── -->
  <section class="card">
    <h2>Existing Applications</h2>
    {#if applications.length === 0}
      <p class="muted">No applications found.</p>
    {:else}
      <ul class="app-list">
        {#each applications as item (applicationExecutionKey(item))}
          <li>
            <button
              type="button"
              class="app-item"
              class:app-item--active={app?.applicationId ===
                item.applicationId && app?.runId === item.runId}
              onclick={() =>
                void loadApplication(item.applicationId, item.runId)}
              disabled={loadLoading}
            >
              <div class="app-item-info">
                <span class="app-item-name">{item.applicantName}</span>
                <span class="mono app-item-id">{item.applicationId}</span>
              </div>
              <div class="app-item-meta">
                <span
                  class="badge"
                  style={workflowVersionStyle(
                    item.workflowVersion ?? 'unknown',
                  )}
                  title={item.workerBuildId
                    ? `Worker Build ID: ${item.workerBuildId}`
                    : 'Workflow version'}
                >
                  {workflowVersionLabel(item.workflowVersion ?? 'unknown')}
                </span>
                <span
                  class="badge"
                  style={workflowStatusStyle(item.workflowStatus)}
                >
                  {workflowStatusLabel(item.workflowStatus)}
                </span>
                {#if item.scenario}
                  <span class="app-item-scenario">{item.scenario}</span>
                {/if}
              </div>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
    {#if loadError}
      <p class="error" style="margin-top:10px">{loadError}</p>
    {/if}
  </section>

  <!-- ── Application panels ────────────────────────────────────────────── -->
  {#if app}
    <div class="two-col">
      <ApplicationSummary
        {app}
        {isTerminal}
        {refreshing}
        {refreshError}
        onRefresh={doRefresh}
        {refreshTimeout}
      />
      <ActionsPanel
        {app}
        {isTerminal}
        {isCreditCheckPending}
        {isPropertyValuationPending}
        onRefresh={doRefresh}
        onRerun={handleRerun}
      />
    </div>

    <AuditTimeline timeline={app.timeline} />
  {/if}
</div>

<style>
  /* ── Page layout ────────────────────────────────────────────────────────── */
  .console {
    max-width: 1200px;
    margin: 0 auto;
    padding: 24px 16px 48px;
    display: flex;
    flex-direction: column;
    gap: 20px;
  }

  .logo {
    width: 50px;
  }

  header {
    padding-bottom: 16px;
    border-bottom: 2px solid #e5e7eb;
  }

  .header-inner {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .title-block {
    display: flex;
    flex-direction: column;
  }

  header h1 {
    font-size: 20px;
    font-weight: 700;
    color: #111827;
  }

  header h1 a {
    color: inherit;
    text-decoration: none;
  }

  .subtitle {
    font-size: 13px;
    color: #6b7280;
  }

  .two-col {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
  }

  @media (max-width: 700px) {
    .two-col {
      grid-template-columns: 1fr;
    }
  }

  /* ── Start form layout ──────────────────────────────────────────────────── */
  .start-form {
    display: flex;
    flex-direction: column;
    gap: 0;
  }

  .form-bank {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0 20px;
    align-items: start;
    padding-bottom: 20px;
  }

  @media (max-width: 700px) {
    .form-bank {
      grid-template-columns: 1fr;
    }
  }

  /* ── Demo controls section ───────────────────────────────────────────────── */
  .demo-controls {
    border-top: 1px solid #e5e7eb;
    padding-top: 20px;
  }

  .demo-controls-title {
    font-size: 13px;
    font-weight: 600;
    color: #374151;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin: 0 0 6px;
  }

  .demo-controls-hint {
    font-size: 12px;
    color: #6b7280;
    margin: 0 0 16px;
  }

  .demo-fields {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0 20px;
    align-items: start;
  }

  @media (max-width: 700px) {
    .demo-fields {
      grid-template-columns: 1fr;
    }
  }

  .rate-value {
    font-weight: 600;
    color: #1d4ed8;
  }

  input[type='range'] {
    width: 100%;
    accent-color: #1d4ed8;
  }

  .input-row {
    display: flex;
    gap: 6px;
  }

  .input-row input {
    flex: 1;
    min-width: 0;
  }

  /* ── Applications list ──────────────────────────────────────────────────── */
  .app-list {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .app-item {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 12px;
    width: 100%;
    padding: 10px 12px;
    border-radius: 6px;
    border: 1px solid #e5e7eb;
    background: white;
    cursor: pointer;
    text-align: left;
  }

  .app-item:hover:not(:disabled) {
    border-color: #93c5fd;
    background: #f0f9ff;
  }

  .app-item--active {
    border-color: #3b82f6;
    background: #eff6ff;
  }

  .app-item:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .app-item-info {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }

  .app-item-name {
    font-size: 14px;
    font-weight: 500;
    color: #111827;
  }

  .app-item-id {
    font-size: 11px;
    color: #6b7280;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .app-item-meta {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-shrink: 0;
  }

  .app-item-scenario {
    font-size: 11px;
    color: #6b7280;
    white-space: nowrap;
  }
</style>
