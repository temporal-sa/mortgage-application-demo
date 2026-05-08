export type ApplicationStatus =
  | 'submitted'
  | 'credit_check_pending'
  | 'offer_reserved'
  | 'completed'
  | 'rejected'
  | 'compensation_required'
  | 'compensated';

export type TimelineStatus = 'started' | 'completed' | 'failed' | 'waiting';

export interface TimelineEntry {
  step: string;
  status: TimelineStatus;
  timestamp: string;
  details?: string;
  metadata?: Record<string, string>;
}

export type SlaStatus = 'within_sla' | 'sla_breached';

// Application-level workflow lifecycle status. The API normalises Temporal's
// raw status names into this enum at the system boundary; the UI never sees
// or compares against Temporal-specific strings.
export type ApplicationWorkflowStatus =
  | 'running'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'terminated'
  | 'timed_out'
  | 'continued_as_new'
  | 'unknown';

// Workflow version derived from the Worker Build ID by the API. v1 has no
// Property Valuation step; v2 includes Property Valuation between Credit &
// AML and Offer Reservation. `unknown` is shown for unversioned workers or
// future versions the UI does not yet recognise.
export type WorkflowVersion = 'v1' | 'v2' | 'unknown';

export interface MortgageApplication {
  applicationId: string;
  applicantName: string;
  status: ApplicationStatus;
  currentStep: string;
  offerId?: string;
  // Operator-supplied property value in pounds. Populated by the v2 workflow
  // once the property-valuation-submitted signal has been received. v1
  // applications never populate this field.
  propertyValue?: number;
  createdAt: string;
  updatedAt: string;
  timeline: TimelineEntry[];
  pendingDependency?: string;
  pendingSince?: string;
  slaDeadline?: string;
  slaStatus?: SlaStatus;
  slaBreached?: boolean;
  workflowStatus?: ApplicationWorkflowStatus;
  workflowVersion?: WorkflowVersion;
  workerBuildId?: string;
  // Temporal run ID that produced this state. Together with applicationId
  // it uniquely identifies a single workflow execution.
  runId?: string;
}

export interface ScenarioOption {
  name: string;
  description: string;
}

export interface ApplicationListItem {
  applicationId: string;
  applicantName: string;
  scenario?: string;
  workflowStatus: ApplicationWorkflowStatus;
  workflowVersion?: WorkflowVersion;
  workerBuildId?: string;
  // Temporal run ID for this execution. The same applicationId can appear
  // across multiple runs (e.g. after a reset/re-run from the Temporal UI),
  // so list items are keyed by (applicationId, runId).
  runId?: string;
}

// Stable execution-level identity for a list item. Built from the
// (applicationId, runId) pair so multiple executions sharing the same
// applicationId can be addressed and rendered distinctly.
export function applicationExecutionKey(
  item: Pick<ApplicationListItem, 'applicationId' | 'runId'>,
): string {
  return `${item.applicationId}:${item.runId ?? ''}`;
}
