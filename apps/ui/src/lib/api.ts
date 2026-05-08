import type {
  ApplicationListItem,
  MortgageApplication,
  ScenarioOption,
} from './types';

const BASE = '/api';

async function request<T>(
  path: string,
  init?: RequestInit,
  fetchFn?: typeof fetch,
): Promise<T> {
  const fn = fetchFn ?? fetch;
  const headers = new Headers(init?.headers);
  if (init?.body !== undefined && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }
  const res = await fn(`${BASE}${path}`, { ...init, headers });

  if (!res.ok) {
    let message = `HTTP ${res.status}`;
    try {
      const body = await res.json();
      if (body?.message) {
        message = Array.isArray(body.message)
          ? body.message.join(', ')
          : String(body.message);
      }
    } catch {
      // ignore parse failure
    }
    throw new Error(message);
  }

  const text = await res.text();
  if (!text) return undefined as T;
  return JSON.parse(text) as T;
}

export async function getApplications(
  fetchFn?: typeof fetch,
): Promise<ApplicationListItem[]> {
  return request<ApplicationListItem[]>('/v1/applications', undefined, fetchFn);
}

export async function getScenarios(
  fetchFn?: typeof fetch,
): Promise<ScenarioOption[]> {
  const { scenarios } = await request<{ scenarios: ScenarioOption[] }>(
    '/v1/applications/scenarios',
    undefined,
    fetchFn,
  );

  return scenarios;
}

export interface StartApplicationPayload {
  applicationId: string;
  applicantName: string;
  scenario: string;
  externalFailureRatePercent?: number;
}

export async function startApplication(
  payload: StartApplicationPayload,
): Promise<void> {
  await request<void>('/v1/applications', {
    method: 'POST',
    body: JSON.stringify(payload),
  });
}

export async function getApplication(
  applicationId: string,
  runId?: string,
  fetchFn?: typeof fetch,
): Promise<MortgageApplication> {
  const query = runId ? `?runId=${encodeURIComponent(runId)}` : '';
  return request<MortgageApplication>(
    `/v1/applications/${encodeURIComponent(applicationId)}${query}`,
    undefined,
    fetchFn,
  );
}

export type ApplicationAction =
  | {
      type: 'submit_credit_check_result';
      payload: { result: 'approved' | 'rejected'; reference?: string };
    }
  | { type: 'retry_credit_check' }
  | { type: 'rerun_application' }
  | {
      type: 'submit_property_valuation';
      propertyValuation: { propertyValue: number };
    };

export async function performAction(
  applicationId: string,
  action: ApplicationAction,
): Promise<{ applicationId: string; workflowId: string } | undefined> {
  return request<{ applicationId: string; workflowId: string } | undefined>(
    `/v1/applications/${encodeURIComponent(applicationId)}/actions`,
    { method: 'POST', body: JSON.stringify(action) },
  );
}
