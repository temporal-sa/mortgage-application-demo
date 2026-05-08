import {
  BadRequestException,
  ConflictException,
  Inject,
  Injectable,
  Logger,
  NotFoundException,
} from '@nestjs/common';
import { Client, WorkflowNotFoundError } from '@temporalio/client';
import type { WorkflowExecutionInfo } from '@temporalio/client/lib/types';
import * as proto from '@temporalio/proto';
import { randomUUID } from 'node:crypto';

import { WORKFLOW_CLIENT } from '../temporal/temporal.providers';
import { ApplicationActionDto } from './dto/application-action.dto';
import { ApplicationListItemDto } from './dto/application-list-item.dto';
import { CreditCheckResult } from './events/credit-check.event';
import {
  ApplicationWorkflowStatus,
  normaliseWorkflowStatus,
} from './models/application-workflow-status.type';
import { MortgageApplication } from './models/mortgage-application.model';
import { MortgageScenario } from './models/mortgage-scenario.type';
import { extractWorkerBuildId } from './models/worker-build-id';
import { deriveWorkflowVersion } from './models/workflow-version.type';

// The workflow type name is the short function name that the Temporal Go SDK
// derives from runtime.FuncForPC. It must match the Go worker registration.
const WORKFLOW_TYPE = 'MortgageApplicationWorkflow';
const TASK_QUEUE = 'mortgage-application';
const SIGNAL_CREDIT_CHECK_COMPLETED = 'credit-check-completed';
const SIGNAL_RETRY_CREDIT_CHECK = 'retry-credit-check';
const SIGNAL_PROPERTY_VALUATION_SUBMITTED = 'property-valuation-submitted';
const QUERY_GET_APPLICATION = 'getApplication';

const STATUS_RUNNING =
  proto.temporal.api.enums.v1.WorkflowExecutionStatus
    .WORKFLOW_EXECUTION_STATUS_RUNNING;

@Injectable()
export class MortgageService {
  protected readonly logger = new Logger(this.constructor.name);

  constructor(@Inject(WORKFLOW_CLIENT) private readonly client: Client) {}

  workflowId(applicationId: string): string {
    return `mortgage-application-${applicationId}`;
  }

  async completeCreditCheck(
    applicationId: string,
    result: CreditCheckResult,
    reference?: string,
  ): Promise<void> {
    if (!(await this.isWorkflowRunning(this.workflowId(applicationId)))) {
      throw new NotFoundException(`Application ${applicationId} not found`);
    }

    const handle = this.client.workflow.getHandle(
      this.workflowId(applicationId),
    );
    await handle.signal(SIGNAL_CREDIT_CHECK_COMPLETED, {
      applicationId,
      result,
      ...(reference !== undefined && { reference }),
    });
  }

  async getApplication(
    applicationId: string,
    runId?: string,
  ): Promise<MortgageApplication> {
    // When runId is supplied the caller wants a specific execution. This
    // matters when the same applicationId has been reset/re-run from the
    // Temporal UI, so multiple executions share the workflowId and the
    // default getHandle would always return the latest.
    const handle = this.client.workflow.getHandle(
      this.workflowId(applicationId),
      runId,
    );
    try {
      // Run describe and query in parallel. workflowStatus lets the UI
      // distinguish a running workflow from one that has been terminated,
      // cancelled or otherwise stopped externally — important so the SLA
      // display can stop ticking when the workflow is no longer running.
      const [desc, app] = await Promise.all([
        handle.describe(),
        handle.query<MortgageApplication>(QUERY_GET_APPLICATION),
      ]);
      // desc.raw is a DescribeWorkflowExecutionResponse, with the
      // WorkflowExecutionInfo (which carries the versioning fields) nested
      // under workflowExecutionInfo. Pass that nested info to the helper so
      // both the describe and list paths use the same extraction logic.
      const workerBuildId = extractWorkerBuildId(
        desc.raw?.workflowExecutionInfo ?? undefined,
      );
      return {
        ...app,
        workflowStatus: normaliseWorkflowStatus(desc.status.code),
        workflowVersion: deriveWorkflowVersion(workerBuildId),
        ...(workerBuildId !== undefined && { workerBuildId }),
        ...(desc.runId !== undefined && { runId: desc.runId }),
      };
    } catch (err) {
      if (err instanceof WorkflowNotFoundError) {
        throw new NotFoundException(`Application ${applicationId} not found`);
      }
      throw err;
    }
  }

  async isWorkflowRunning(workflowId: string): Promise<boolean> {
    this.logger.debug({ workflowId }, 'Checking if workflow is running');

    try {
      const desc = await this.client.workflow.getHandle(workflowId).describe();
      return desc.status.code === STATUS_RUNNING;
    } catch {
      return false;
    }
  }

  async workflowExists(workflowId: string): Promise<boolean> {
    this.logger.debug({ workflowId }, 'Checking if workflow exists');

    try {
      await this.client.workflow.getHandle(workflowId).describe();
      return true;
    } catch {
      return false;
    }
  }

  async startApplication(
    applicationId: string,
    applicantName: string,
    scenario?: MortgageScenario,
    externalFailureRatePercent?: number,
  ): Promise<{ workflowId: string; applicationId: string }> {
    const workflowId = this.workflowId(applicationId);

    if (await this.workflowExists(workflowId)) {
      throw new ConflictException(
        `Workflow already exists for applicationId: ${applicationId}`,
      );
    }

    this.logger.log(
      { workflowId, applicationId },
      'Starting mortgage application workflow',
    );

    const resolvedScenario = scenario ?? 'happy_path';
    const resolvedFailureRate = this.allowsFailureInjection(resolvedScenario)
      ? (externalFailureRatePercent ?? 0)
      : 0;

    await this.client.workflow.start(WORKFLOW_TYPE, {
      taskQueue: TASK_QUEUE,
      workflowId,
      memo: {
        applicationId,
        applicantName,
        scenario: resolvedScenario,
        externalFailureRatePercent: resolvedFailureRate,
      },
      args: [
        {
          applicationId,
          applicantName,
          submittedAt: new Date().toISOString(),
          scenario: resolvedScenario,
          externalFailureRatePercent: resolvedFailureRate,
        },
      ],
    });

    return { workflowId, applicationId };
  }

  async retryCreditCheck(applicationId: string): Promise<void> {
    if (!(await this.isWorkflowRunning(this.workflowId(applicationId)))) {
      throw new NotFoundException(`Application ${applicationId} not found`);
    }

    this.logger.log(
      { applicationId },
      'Operator retry: sending retry-credit-check signal',
    );

    const handle = this.client.workflow.getHandle(
      this.workflowId(applicationId),
    );
    await handle.signal(SIGNAL_RETRY_CREDIT_CHECK);
  }

  // submitPropertyValuation signals the v2 workflow with the operator-supplied
  // property value. Validation is performed by the DTO (number, positive) and
  // re-asserted here so a malformed direct service call cannot bypass the
  // boundary checks. The workflow itself defensively ignores non-positive
  // submissions, but the API rejects them up front so the operator gets a
  // clear error rather than a silent no-op. Sent at any time on a running
  // workflow: v1 does not register the signal so a misrouted submission is
  // harmlessly buffered until the workflow closes.
  async submitPropertyValuation(
    applicationId: string,
    propertyValue: number,
  ): Promise<void> {
    if (typeof propertyValue !== 'number' || !Number.isFinite(propertyValue)) {
      throw new BadRequestException(
        'propertyValue must be a finite number in pounds',
      );
    }
    if (propertyValue <= 0) {
      throw new BadRequestException('propertyValue must be a positive number');
    }

    if (!(await this.isWorkflowRunning(this.workflowId(applicationId)))) {
      throw new NotFoundException(`Application ${applicationId} not found`);
    }

    this.logger.log(
      { applicationId, propertyValue },
      'Sending property-valuation-submitted signal',
    );

    const handle = this.client.workflow.getHandle(
      this.workflowId(applicationId),
    );
    await handle.signal(SIGNAL_PROPERTY_VALUATION_SUBMITTED, {
      applicationId,
      propertyValue,
    });
  }

  async rerunApplication(
    applicationId: string,
  ): Promise<{ applicationId: string; workflowId: string }> {
    const existingWorkflowId = this.workflowId(applicationId);

    let applicantName = '';
    let scenario = 'happy_path';
    let externalFailureRatePercent = 0;

    try {
      const desc = await this.client.workflow
        .getHandle(existingWorkflowId)
        .describe();
      const memo = this.readMemo(desc.memo);
      applicantName = memo.applicantName ?? '';
      scenario = memo.scenario ?? 'happy_path';
      externalFailureRatePercent = this.allowsFailureInjection(scenario)
        ? (memo.externalFailureRatePercent ?? 0)
        : 0;
    } catch (err) {
      if (err instanceof WorkflowNotFoundError) {
        throw new NotFoundException(`Application ${applicationId} not found`);
      }
      throw err;
    }

    const newApplicationId = randomUUID();
    const newWorkflowId = this.workflowId(newApplicationId);

    this.logger.log(
      { applicationId, newApplicationId },
      'Operator rerun: starting new workflow',
    );

    await this.client.workflow.start(WORKFLOW_TYPE, {
      taskQueue: TASK_QUEUE,
      workflowId: newWorkflowId,
      memo: {
        applicationId: newApplicationId,
        applicantName,
        scenario,
        externalFailureRatePercent,
      },
      args: [
        {
          applicationId: newApplicationId,
          applicantName,
          submittedAt: new Date().toISOString(),
          scenario,
          originalApplicationId: applicationId,
          externalFailureRatePercent,
        },
      ],
    });

    return { applicationId: newApplicationId, workflowId: newWorkflowId };
  }

  async handleAction(
    applicationId: string,
    action: ApplicationActionDto,
  ): Promise<{ applicationId: string; workflowId: string } | void> {
    switch (action.type) {
      case 'submit_credit_check_result':
        if (!action.payload) {
          throw new BadRequestException(
            'payload is required for submit_credit_check_result',
          );
        }
        return this.completeCreditCheck(
          applicationId,
          action.payload.result,
          action.payload.reference,
        );
      case 'retry_credit_check':
        return this.retryCreditCheck(applicationId);
      case 'rerun_application':
        return this.rerunApplication(applicationId);
      case 'submit_property_valuation':
        if (!action.propertyValuation) {
          throw new BadRequestException(
            'propertyValuation is required for submit_property_valuation',
          );
        }
        return this.submitPropertyValuation(
          applicationId,
          action.propertyValuation.propertyValue,
        );
    }
  }

  async listApplications(): Promise<ApplicationListItemDto[]> {
    const applications: ApplicationListItemDto[] = [];

    try {
      for await (const info of this.client.workflow.list({
        query: `WorkflowType = "${WORKFLOW_TYPE}"`,
      })) {
        try {
          applications.push(await this.resolveListItem(info));
        } catch {
          this.logger.warn(
            { workflowId: info.workflowId },
            'Failed to retrieve application details',
          );
        }
      }
    } catch {
      this.logger.warn('Failed to list applications from Temporal');
    }

    return applications;
  }

  private async resolveListItem(
    info: WorkflowExecutionInfo,
  ): Promise<ApplicationListItemDto> {
    // Normalise once at the Temporal boundary so the rest of this method
    // (and everything downstream of it) only deals with the application-level
    // status. Reading `status.code` (the proto enum value) avoids any
    // dependency on the SDK's spelling of the equivalent name.
    const workflowStatus = normaliseWorkflowStatus(info.status.code);
    const memo = this.readMemo(info.memo);
    const handle = this.client.workflow.getHandle(info.workflowId);

    // Worker Build ID lives on the raw proto. Visibility data (info.raw) is
    // not a reliable source: ListWorkflowExecutions is served from the
    // visibility store, which does not always populate the Worker
    // Deployment Versioning fields (versioningInfo / assignedBuildId). The
    // detail path reaches the same data via handle.describe(), which goes
    // to the workflow shard. Calling describe() here too gives the list
    // and detail responses the same source of truth for workflowVersion
    // and workerBuildId. The describe call is allowed to fail silently —
    // if it does, the list still renders with workflowVersion=unknown
    // rather than failing the whole request.
    const workerBuildId = await this.resolveWorkerBuildId(info, handle);
    const runId = info.runId;

    if (memo.applicantName !== undefined) {
      return this.toApplicationListItem(
        info.workflowId,
        workflowStatus,
        memo,
        workerBuildId,
        runId,
      );
    }

    // Legacy workflows without memo — fall back to query or result
    if (workflowStatus === 'running') {
      const app = await handle.query<MortgageApplication>(
        QUERY_GET_APPLICATION,
      );
      return this.toApplicationListItem(
        info.workflowId,
        workflowStatus,
        {
          applicationId: app.applicationId,
          applicantName: app.applicantName,
        },
        workerBuildId,
        runId,
      );
    }

    if (workflowStatus === 'completed') {
      const app = (await handle.result()) as MortgageApplication;
      return this.toApplicationListItem(
        info.workflowId,
        workflowStatus,
        {
          applicationId: app.applicationId,
          applicantName: app.applicantName,
        },
        workerBuildId,
        runId,
      );
    }

    return this.toApplicationListItem(
      info.workflowId,
      workflowStatus,
      {},
      workerBuildId,
      runId,
    );
  }

  // resolveWorkerBuildId tries the visibility data first (cheap; works when
  // the visibility store has versioningInfo/assignedBuildId populated) and
  // falls back to a describe call (one extra round trip per list item, but
  // matches the detail path exactly). Errors from describe are swallowed:
  // a deleted/race-condition workflow should not break the whole list.
  private async resolveWorkerBuildId(
    info: WorkflowExecutionInfo,
    handle: ReturnType<Client['workflow']['getHandle']>,
  ): Promise<string | undefined> {
    const fromVisibility = extractWorkerBuildId(info.raw);
    if (fromVisibility) return fromVisibility;

    try {
      const desc = await handle.describe();
      return extractWorkerBuildId(desc.raw?.workflowExecutionInfo ?? undefined);
    } catch {
      return undefined;
    }
  }

  private allowsFailureInjection(scenario: string): boolean {
    return scenario === 'happy_path';
  }

  private readMemo(memo: Record<string, unknown> | undefined): {
    applicationId?: string;
    applicantName?: string;
    scenario?: string;
    externalFailureRatePercent?: number;
  } {
    if (!memo) return {};
    return {
      applicationId:
        typeof memo['applicationId'] === 'string'
          ? memo['applicationId']
          : undefined,
      applicantName:
        typeof memo['applicantName'] === 'string'
          ? memo['applicantName']
          : undefined,
      scenario:
        typeof memo['scenario'] === 'string' ? memo['scenario'] : undefined,
      externalFailureRatePercent:
        typeof memo['externalFailureRatePercent'] === 'number'
          ? memo['externalFailureRatePercent']
          : undefined,
    };
  }

  private toApplicationListItem(
    workflowId: string,
    workflowStatus: ApplicationWorkflowStatus,
    data: { applicationId?: string; applicantName?: string; scenario?: string },
    workerBuildId?: string,
    runId?: string,
  ): ApplicationListItemDto {
    return {
      applicationId:
        data.applicationId ?? workflowId.replace('mortgage-application-', ''),
      applicantName: data.applicantName ?? '',
      scenario: data.scenario,
      workflowStatus,
      workflowVersion: deriveWorkflowVersion(workerBuildId),
      ...(workerBuildId !== undefined && { workerBuildId }),
      ...(runId !== undefined && { runId }),
    };
  }
}
