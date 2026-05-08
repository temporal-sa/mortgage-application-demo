import { ApiProperty, ApiPropertyOptional } from '@nestjs/swagger';

import { APPLICATION_WORKFLOW_STATUSES } from '../models/application-workflow-status.type';
import type { ApplicationWorkflowStatus } from '../models/application-workflow-status.type';
import { WORKFLOW_VERSIONS } from '../models/workflow-version.type';
import type { WorkflowVersion } from '../models/workflow-version.type';

export class ApplicationListItemDto {
  @ApiProperty({ description: 'Unique identifier for the application' })
  applicationId: string;

  @ApiPropertyOptional({
    description:
      'Temporal workflow run ID for this execution. Combined with applicationId it uniquely identifies a single workflow execution, which matters when the same applicationId has been re-run.',
  })
  runId?: string;

  @ApiProperty({ description: 'Full name of the applicant' })
  applicantName: string;

  @ApiPropertyOptional({ description: 'Demo scenario for this application' })
  scenario?: string;

  @ApiProperty({
    description:
      'Application-level workflow lifecycle status, normalised from the underlying Temporal execution status',
    enum: APPLICATION_WORKFLOW_STATUSES,
    example: 'running',
  })
  workflowStatus: ApplicationWorkflowStatus;

  @ApiPropertyOptional({
    description:
      'Workflow version (derived from the Worker Build ID): v1, v2, or unknown',
    enum: WORKFLOW_VERSIONS,
    example: 'v1',
  })
  workflowVersion?: WorkflowVersion;

  @ApiPropertyOptional({
    description:
      'Raw Worker Build ID Temporal recorded for this execution, when available',
    example: 'mortgage-worker-v1',
  })
  workerBuildId?: string;
}
