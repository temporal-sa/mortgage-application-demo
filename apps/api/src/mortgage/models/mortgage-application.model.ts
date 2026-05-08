import { ApplicationStatus } from './application-status.type.js';
import { ApplicationWorkflowStatus } from './application-workflow-status.type.js';
import { TimelineEntry } from './timeline-entry.model.js';
import { WorkflowVersion } from './workflow-version.type.js';

export type SlaStatus = 'within_sla' | 'sla_breached';

export interface MortgageApplication {
  applicationId: string;
  applicantName: string;
  status: ApplicationStatus;
  currentStep: string;
  offerId?: string;
  // Operator-supplied property value in pounds, populated by the v2 workflow
  // once the property-valuation-submitted signal has been received. v1
  // workflows never populate this field.
  propertyValue?: number;
  createdAt: string;
  updatedAt: string;
  timeline: TimelineEntry[];
  pendingDependency?: string;
  pendingSince?: string;
  slaDeadline?: string;
  slaStatus?: SlaStatus;
  slaBreached?: boolean;
  // Application-level workflow lifecycle status. The API normalises Temporal's
  // raw status names into this enum at the boundary so the UI can stop showing
  // live SLA visuals when the workflow is no longer running, without ever
  // depending on Temporal-specific naming.
  workflowStatus?: ApplicationWorkflowStatus;
  // Workflow version derived from the Worker Build ID Temporal records on
  // the execution (e.g. `mortgage-worker-v1` -> `v1`). Drives the
  // versioning-aware UI badges and is the same value v1 executions retain
  // after v2 has been promoted as the current Worker Deployment Version.
  workflowVersion?: WorkflowVersion;
  // Raw Worker Build ID reported by Temporal for this execution. Useful for
  // operator-level visibility in the UI summary; may be absent for
  // workflows that ran on an unversioned worker.
  workerBuildId?: string;
  // Temporal run ID for this workflow execution. Together with applicationId
  // it uniquely identifies a single execution; the same applicationId can
  // appear across multiple runs (e.g. after a reset/re-run from the
  // Temporal UI).
  runId?: string;
}
