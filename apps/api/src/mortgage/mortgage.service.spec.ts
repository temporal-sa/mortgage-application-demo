import { ConflictException, NotFoundException } from '@nestjs/common';
import { Test, TestingModule } from '@nestjs/testing';
import { WorkflowNotFoundError } from '@temporalio/client';

import { WORKFLOW_CLIENT } from '../temporal/temporal.providers';
import { WorkflowExecutionStatus } from './models/application-workflow-status.type';
import { MortgageService } from './mortgage.service';

// Tests pass the actual Temporal proto enum values to the mocked describe()
// so the normaliser is exercised the same way it is in production. No raw
// status strings appear in this file.
const STATUS_RUNNING = {
  status: { code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING },
};

describe('MortgageService', () => {
  let service: MortgageService;

  const mockHandle = {
    query: jest.fn(),
    signal: jest.fn(),
    describe: jest.fn(),
  };

  const mockWorkflowClient = {
    workflow: {
      start: jest.fn(),
      getHandle: jest.fn(),
    },
  };

  beforeEach(async () => {
    const module: TestingModule = await Test.createTestingModule({
      providers: [
        MortgageService,
        { provide: WORKFLOW_CLIENT, useValue: mockWorkflowClient },
      ],
    }).compile();

    service = module.get<MortgageService>(MortgageService);

    jest.clearAllMocks();
    mockWorkflowClient.workflow.start.mockResolvedValue(mockHandle);
    mockWorkflowClient.workflow.getHandle.mockReturnValue(mockHandle);
    // Default: workflow is running. Override per test where a different state is needed.
    mockHandle.describe.mockResolvedValue(STATUS_RUNNING);
  });

  describe('startApplication', () => {
    it('starts the workflow with correct type, workflow ID, and task queue', async () => {
      mockHandle.describe.mockRejectedValue(new Error('not found'));

      const result = await service.startApplication('app-123', 'John Smith');

      expect(mockWorkflowClient.workflow.start).toHaveBeenCalledWith(
        'MortgageApplicationWorkflow',
        expect.objectContaining({
          taskQueue: 'mortgage-application',
          workflowId: 'mortgage-application-app-123',
          args: [
            expect.objectContaining({
              applicationId: 'app-123',
              applicantName: 'John Smith',
            }),
          ],
        }),
      );
      expect(result).toEqual({
        workflowId: 'mortgage-application-app-123',
        applicationId: 'app-123',
      });
    });

    it('sends happy_path scenario when no scenario is specified', async () => {
      mockHandle.describe.mockRejectedValue(new Error('not found'));

      await service.startApplication('app-123', 'John Smith');

      expect(mockWorkflowClient.workflow.start).toHaveBeenCalledWith(
        'MortgageApplicationWorkflow',
        expect.objectContaining({
          args: [expect.objectContaining({ scenario: 'happy_path' })],
        }),
      );
    });

    it('sends happy_path scenario when happy_path is specified', async () => {
      mockHandle.describe.mockRejectedValue(new Error('not found'));

      await service.startApplication('app-123', 'John Smith', 'happy_path');

      expect(mockWorkflowClient.workflow.start).toHaveBeenCalledWith(
        'MortgageApplicationWorkflow',
        expect.objectContaining({
          args: [expect.objectContaining({ scenario: 'happy_path' })],
        }),
      );
    });

    it('sends fail_after_offer_reservation scenario when specified', async () => {
      mockHandle.describe.mockRejectedValue(new Error('not found'));

      await service.startApplication(
        'app-123',
        'John Smith',
        'fail_after_offer_reservation',
      );

      expect(mockWorkflowClient.workflow.start).toHaveBeenCalledWith(
        'MortgageApplicationWorkflow',
        expect.objectContaining({
          args: [
            expect.objectContaining({
              scenario: 'fail_after_offer_reservation',
            }),
          ],
        }),
      );
    });

    it('sends fail_and_compensate_after_offer_reservation scenario when specified', async () => {
      mockHandle.describe.mockRejectedValue(new Error('not found'));

      await service.startApplication(
        'app-123',
        'John Smith',
        'fail_and_compensate_after_offer_reservation',
      );

      expect(mockWorkflowClient.workflow.start).toHaveBeenCalledWith(
        'MortgageApplicationWorkflow',
        expect.objectContaining({
          args: [
            expect.objectContaining({
              scenario: 'fail_and_compensate_after_offer_reservation',
            }),
          ],
        }),
      );
    });

    it('throws ConflictException when a workflow is already running', async () => {
      // default: mockHandle.describe resolves with STATUS_RUNNING
      await expect(
        service.startApplication('app-123', 'John Smith'),
      ).rejects.toThrow(ConflictException);
    });

    it('throws ConflictException when a workflow already completed', async () => {
      mockHandle.describe.mockResolvedValue({ status: { code: 2 } }); // COMPLETED

      await expect(
        service.startApplication('app-123', 'John Smith'),
      ).rejects.toThrow(ConflictException);
    });
  });

  describe('getApplication', () => {
    it('returns application state merged with the running workflow status', async () => {
      const mockApp = { applicationId: 'app-123', status: 'submitted' };
      mockHandle.query.mockResolvedValue(mockApp);

      const result = await service.getApplication('app-123');

      expect(mockWorkflowClient.workflow.getHandle).toHaveBeenCalledWith(
        'mortgage-application-app-123',
        undefined,
      );
      expect(mockHandle.query).toHaveBeenCalledWith('getApplication');
      // The API normalises Temporal's proto enum value to the canonical
      // application-level status before returning, so callers never see
      // Temporal-specific constants or naming variants. workflowVersion
      // collapses to 'unknown' here because the mocked describe response
      // carries no Build ID.
      expect(result).toEqual({
        ...mockApp,
        workflowStatus: 'running',
        workflowVersion: 'unknown',
      });
    });

    it('returns application state merged with the completed workflow status', async () => {
      const mockApp = { applicationId: 'app-123', status: 'completed' };
      mockHandle.query.mockResolvedValue(mockApp);
      mockHandle.describe.mockResolvedValue({
        status: {
          code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_COMPLETED,
        },
      });

      const result = await service.getApplication('app-123');

      expect(result).toEqual({
        ...mockApp,
        workflowStatus: 'completed',
        workflowVersion: 'unknown',
      });
    });

    // Each Temporal proto enum value must normalise to the exact
    // application-level status the UI expects. Tests pass the enum value
    // itself (the same numeric code Temporal returns on `desc.status.code`)
    // rather than relying on the SDK's string name, so the normaliser is
    // exercised against the same input shape it sees in production.
    it.each([
      {
        code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_TERMINATED,
        expected: 'terminated',
      },
      {
        // Temporal's proto enum is the American spelling. The mapping
        // intentionally produces the British `cancelled` for consistency
        // with the rest of the application's vocabulary.
        code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_CANCELED,
        expected: 'cancelled',
      },
      {
        code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
        expected: 'timed_out',
      },
      {
        code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_FAILED,
        expected: 'failed',
      },
      {
        code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW,
        expected: 'continued_as_new',
      },
    ])(
      'normalises Temporal status code=$code to application-level status $expected',
      async ({ code, expected }) => {
        const mockApp = {
          applicationId: 'app-123',
          status: 'credit_check_pending',
          pendingDependency: 'credit_check',
        };
        mockHandle.query.mockResolvedValue(mockApp);
        mockHandle.describe.mockResolvedValue({ status: { code } });

        const result = await service.getApplication('app-123');

        expect(result.workflowStatus).toBe(expected);
        // Mid-flight query data is preserved verbatim; only the lifecycle
        // hint is added on top so the UI can suppress live SLA visuals.
        expect(result).toMatchObject(mockApp);
      },
    );

    // Statuses we do not explicitly map (the `_UNSPECIFIED` / `_PAUSED`
    // proto values, undefined, and any future Temporal additions) collapse
    // to the single `unknown` bucket so the UI never has to handle a value
    // it does not recognise. The 999 case simulates a future Temporal enum
    // value that this codebase has not yet been updated to handle.
    it.each([
      { code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED },
      { code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_PAUSED },
      { code: 999 },
      { code: undefined },
    ])('normalises Temporal status code=$code to unknown', async ({ code }) => {
      const mockApp = { applicationId: 'app-123', status: 'submitted' };
      mockHandle.query.mockResolvedValue(mockApp);
      mockHandle.describe.mockResolvedValue({ status: { code } });

      const result = await service.getApplication('app-123');

      expect(result.workflowStatus).toBe('unknown');
    });

    it('throws NotFoundException when the workflow does not exist', async () => {
      mockHandle.query.mockRejectedValue(
        new WorkflowNotFoundError(
          'workflow not found',
          'mortgage-application-app-123',
          undefined,
        ),
      );

      await expect(service.getApplication('app-123')).rejects.toThrow(
        NotFoundException,
      );
    });

    it('propagates unexpected errors from the query', async () => {
      mockHandle.query.mockRejectedValue(
        new Error('unexpected temporal error'),
      );

      await expect(service.getApplication('app-123')).rejects.toThrow(
        'unexpected temporal error',
      );
    });

    // Versioning metadata is read from the raw proto on desc.raw, where the
    // versioning info is nested under workflowExecutionInfo. The service
    // exposes both the raw build ID (workerBuildId) and the derived
    // application-level version (workflowVersion) so the UI can show a
    // simple v1/v2 badge plus the underlying build for operator visibility.
    it('exposes workflowVersion=v1 and workerBuildId for a v1 worker', async () => {
      mockHandle.query.mockResolvedValue({
        applicationId: 'app-123',
        status: 'submitted',
      });
      mockHandle.describe.mockResolvedValue({
        status: {
          code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
        },
        raw: {
          workflowExecutionInfo: {
            versioningInfo: {
              deploymentVersion: { buildId: 'mortgage-worker-v1' },
            },
          },
        },
      });

      const result = await service.getApplication('app-123');

      expect(result.workflowVersion).toBe('v1');
      expect(result.workerBuildId).toBe('mortgage-worker-v1');
    });

    it('exposes workflowVersion=v2 and workerBuildId for a v2 worker', async () => {
      mockHandle.query.mockResolvedValue({
        applicationId: 'app-123',
        status: 'submitted',
      });
      mockHandle.describe.mockResolvedValue({
        status: {
          code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
        },
        raw: {
          workflowExecutionInfo: {
            versioningInfo: {
              deploymentVersion: { buildId: 'mortgage-worker-v2' },
            },
          },
        },
      });

      const result = await service.getApplication('app-123');

      expect(result.workflowVersion).toBe('v2');
      expect(result.workerBuildId).toBe('mortgage-worker-v2');
    });

    it('falls back to assignedBuildId when versioningInfo is absent', async () => {
      // Older Worker Versioning API still reports the build ID via
      // assignedBuildId rather than versioningInfo.deploymentVersion. The
      // helper prefers the new field but reads the legacy one as a fallback.
      mockHandle.query.mockResolvedValue({
        applicationId: 'app-123',
        status: 'submitted',
      });
      mockHandle.describe.mockResolvedValue({
        status: {
          code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
        },
        raw: {
          workflowExecutionInfo: {
            assignedBuildId: 'mortgage-worker-v2',
          },
        },
      });

      const result = await service.getApplication('app-123');

      expect(result.workflowVersion).toBe('v2');
      expect(result.workerBuildId).toBe('mortgage-worker-v2');
    });

    it('returns workflowVersion=unknown and no workerBuildId when the worker is unversioned', async () => {
      mockHandle.query.mockResolvedValue({
        applicationId: 'app-123',
        status: 'submitted',
      });
      mockHandle.describe.mockResolvedValue({
        status: {
          code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
        },
        raw: { workflowExecutionInfo: {} },
      });

      const result = await service.getApplication('app-123');

      expect(result.workflowVersion).toBe('unknown');
      expect(result.workerBuildId).toBeUndefined();
    });

    it('exposes runId from the workflow execution description', async () => {
      mockHandle.query.mockResolvedValue({
        applicationId: 'app-123',
        status: 'submitted',
      });
      mockHandle.describe.mockResolvedValue({
        status: {
          code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
        },
        runId: 'run-xyz',
      });

      const result = await service.getApplication('app-123');

      expect(result.runId).toBe('run-xyz');
    });

    it('passes runId through to getHandle when supplied', async () => {
      mockHandle.query.mockResolvedValue({
        applicationId: 'app-123',
        status: 'submitted',
      });

      await service.getApplication('app-123', 'run-aaa');

      expect(mockWorkflowClient.workflow.getHandle).toHaveBeenCalledWith(
        'mortgage-application-app-123',
        'run-aaa',
      );
    });
  });

  describe('listApplications', () => {
    function makeListYielder(
      items: Array<{
        workflowId: string;
        statusCode: number;
        memo?: Record<string, unknown>;
        raw?: Record<string, unknown>;
        runId?: string;
      }>,
    ) {
      return {
        // The service consumes this with `for await...of`, so the iterable
        // must be async. The no-op await keeps the linter happy without
        // changing the generator's observable behaviour.
        async *list() {
          await Promise.resolve();
          for (const item of items) {
            yield {
              workflowId: item.workflowId,
              runId: item.runId,
              status: { code: item.statusCode },
              memo: item.memo,
              raw: item.raw ?? {},
            };
          }
        },
      };
    }

    function withListIterator(
      items: Array<{
        workflowId: string;
        statusCode: number;
        memo?: Record<string, unknown>;
        raw?: Record<string, unknown>;
        runId?: string;
      }>,
    ) {
      const yielder = makeListYielder(items);
      (mockWorkflowClient.workflow as unknown as { list: () => unknown }).list =
        yielder.list.bind(yielder);
    }

    it('annotates list items with workflowVersion when visibility populates the build ID', async () => {
      withListIterator([
        {
          workflowId: 'mortgage-application-v1-app',
          statusCode: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
          memo: { applicantName: 'Alice', applicationId: 'v1-app' },
          raw: {
            assignedBuildId: 'mortgage-worker-v1',
          },
        },
        {
          workflowId: 'mortgage-application-v2-app',
          statusCode:
            WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_COMPLETED,
          memo: { applicantName: 'Bob', applicationId: 'v2-app' },
          raw: {
            versioningInfo: {
              deploymentVersion: { buildId: 'mortgage-worker-v2' },
            },
          },
        },
        {
          workflowId: 'mortgage-application-unversioned-app',
          statusCode: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
          memo: { applicantName: 'Carol', applicationId: 'unversioned-app' },
          raw: {},
        },
      ]);
      // The default describe mock has no versioning info, so the fallback
      // describe call leaves the unversioned item as 'unknown' and does not
      // overwrite the build ID for the items where visibility already has
      // it.
      mockHandle.describe.mockResolvedValue(STATUS_RUNNING);

      const result = await service.listApplications();

      expect(result).toEqual([
        expect.objectContaining({
          applicationId: 'v1-app',
          workflowVersion: 'v1',
          workerBuildId: 'mortgage-worker-v1',
        }),
        expect.objectContaining({
          applicationId: 'v2-app',
          workflowVersion: 'v2',
          workerBuildId: 'mortgage-worker-v2',
        }),
        expect.objectContaining({
          applicationId: 'unversioned-app',
          workflowVersion: 'unknown',
        }),
      ]);
      expect(result[2].workerBuildId).toBeUndefined();
    });

    // Production scenario the user reported: visibility data does not
    // populate the Worker Deployment Versioning fields, so info.raw is
    // empty but describe() does carry versioningInfo. The list path must
    // fall back to describe() so list and detail show the same version.
    it('falls back to describe() when visibility omits the build ID', async () => {
      withListIterator([
        {
          workflowId: 'mortgage-application-v2-app',
          statusCode: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
          memo: { applicantName: 'Bob', applicationId: 'v2-app' },
          // Visibility returned no versioning data — exactly what dev-server
          // produces today for Worker Deployment Versioning workflows.
          raw: {},
        },
      ]);
      // describe() returns the rich proto with versioningInfo populated,
      // matching what handle.describe() returns in production.
      mockHandle.describe.mockResolvedValue({
        status: {
          code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
        },
        raw: {
          workflowExecutionInfo: {
            versioningInfo: {
              deploymentVersion: { buildId: 'mortgage-worker-v2' },
            },
          },
        },
      });

      const result = await service.listApplications();

      expect(result).toEqual([
        expect.objectContaining({
          applicationId: 'v2-app',
          workflowVersion: 'v2',
          workerBuildId: 'mortgage-worker-v2',
        }),
      ]);
    });

    // Parity guarantee: for the same workflow, list and detail must agree
    // on workflowVersion and workerBuildId. Without the describe()
    // fallback in resolveListItem the list returned 'unknown' while the
    // detail showed 'v1', which is the exact discrepancy the user reported.
    it('returns the same workflowVersion/workerBuildId as getApplication for the same workflow', async () => {
      const describeResponse = {
        status: {
          code: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
        },
        raw: {
          workflowExecutionInfo: {
            versioningInfo: {
              deploymentVersion: { buildId: 'mortgage-worker-v1' },
            },
          },
        },
      };
      mockHandle.describe.mockResolvedValue(describeResponse);
      mockHandle.query.mockResolvedValue({
        applicationId: 'v1-app',
        applicantName: 'Alice',
        status: 'submitted',
      });

      withListIterator([
        {
          workflowId: 'mortgage-application-v1-app',
          statusCode: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
          memo: { applicantName: 'Alice', applicationId: 'v1-app' },
          // Visibility empty, just like production.
          raw: {},
        },
      ]);

      const [listItem] = await service.listApplications();
      const detail = await service.getApplication('v1-app');

      expect(listItem.workflowVersion).toBe(detail.workflowVersion);
      expect(listItem.workerBuildId).toBe(detail.workerBuildId);
      expect(listItem.workflowVersion).toBe('v1');
      expect(listItem.workerBuildId).toBe('mortgage-worker-v1');
    });

    // The same applicationId can legitimately appear across multiple
    // workflow executions (e.g. after a reset/re-run from the Temporal UI).
    // Each list item must carry its own runId so the UI can key by
    // (applicationId, runId) rather than by applicationId alone.
    it('includes runId on each list item and returns duplicates with the same applicationId distinctly', async () => {
      withListIterator([
        {
          workflowId: 'mortgage-application-app-123',
          runId: 'run-aaa',
          statusCode: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
          memo: { applicantName: 'Alice', applicationId: 'app-123' },
          raw: {},
        },
        {
          workflowId: 'mortgage-application-app-123',
          runId: 'run-bbb',
          statusCode:
            WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_COMPLETED,
          memo: { applicantName: 'Alice', applicationId: 'app-123' },
          raw: {},
        },
      ]);

      const result = await service.listApplications();

      expect(result).toHaveLength(2);
      expect(result[0]).toEqual(
        expect.objectContaining({
          applicationId: 'app-123',
          runId: 'run-aaa',
          workflowStatus: 'running',
        }),
      );
      expect(result[1]).toEqual(
        expect.objectContaining({
          applicationId: 'app-123',
          runId: 'run-bbb',
          workflowStatus: 'completed',
        }),
      );
    });

    // describe() can fail (e.g. workflow deleted between list and
    // describe). The list must still return — with workflowVersion as
    // 'unknown' for that item — rather than failing the whole request.
    it('tolerates describe() failure on the fallback path', async () => {
      withListIterator([
        {
          workflowId: 'mortgage-application-broken-app',
          statusCode: WorkflowExecutionStatus.WORKFLOW_EXECUTION_STATUS_RUNNING,
          memo: { applicantName: 'Eve', applicationId: 'broken-app' },
          raw: {},
        },
      ]);
      mockHandle.describe.mockRejectedValue(new Error('not found'));

      const result = await service.listApplications();

      expect(result).toEqual([
        expect.objectContaining({
          applicationId: 'broken-app',
          workflowVersion: 'unknown',
        }),
      ]);
      expect(result[0].workerBuildId).toBeUndefined();
    });
  });

  describe('submitPropertyValuation', () => {
    it('signals the workflow with the operator-supplied property value', async () => {
      await service.submitPropertyValuation('app-123', 350000);

      expect(mockWorkflowClient.workflow.getHandle).toHaveBeenCalledWith(
        'mortgage-application-app-123',
      );
      expect(mockHandle.signal).toHaveBeenCalledWith(
        'property-valuation-submitted',
        { applicationId: 'app-123', propertyValue: 350000 },
      );
    });

    it('rejects a non-finite property value', async () => {
      await expect(
        service.submitPropertyValuation('app-123', Number.NaN),
      ).rejects.toThrow('finite number');
      expect(mockHandle.signal).not.toHaveBeenCalled();
    });

    it('rejects zero', async () => {
      await expect(
        service.submitPropertyValuation('app-123', 0),
      ).rejects.toThrow('positive');
      expect(mockHandle.signal).not.toHaveBeenCalled();
    });

    it('rejects a negative property value', async () => {
      await expect(
        service.submitPropertyValuation('app-123', -1),
      ).rejects.toThrow('positive');
      expect(mockHandle.signal).not.toHaveBeenCalled();
    });

    it('throws NotFoundException when the workflow is not running', async () => {
      mockHandle.describe.mockRejectedValue(new Error('not found'));

      await expect(
        service.submitPropertyValuation('app-123', 350000),
      ).rejects.toThrow(NotFoundException);
      expect(mockHandle.signal).not.toHaveBeenCalled();
    });
  });

  describe('handleAction (submit_property_valuation)', () => {
    it('routes submit_property_valuation to submitPropertyValuation with the supplied value', async () => {
      await service.handleAction('app-123', {
        type: 'submit_property_valuation',
        propertyValuation: { propertyValue: 350000 },
      });

      expect(mockHandle.signal).toHaveBeenCalledWith(
        'property-valuation-submitted',
        { applicationId: 'app-123', propertyValue: 350000 },
      );
    });

    it('rejects when propertyValuation is missing on the action', async () => {
      await expect(
        service.handleAction('app-123', {
          type: 'submit_property_valuation',
        }),
      ).rejects.toThrow('propertyValuation is required');
      expect(mockHandle.signal).not.toHaveBeenCalled();
    });
  });

  describe('completeCreditCheck', () => {
    it('sends credit-check-completed signal with correct payload', async () => {
      await service.completeCreditCheck('app-123', 'approved', 'REF-001');

      expect(mockWorkflowClient.workflow.getHandle).toHaveBeenCalledWith(
        'mortgage-application-app-123',
      );
      expect(mockHandle.signal).toHaveBeenCalledWith(
        'credit-check-completed',
        expect.objectContaining({
          applicationId: 'app-123',
          result: 'approved',
          reference: 'REF-001',
        }),
      );
    });

    it('sends the signal without reference when omitted', async () => {
      await service.completeCreditCheck('app-123', 'rejected');

      expect(mockHandle.signal).toHaveBeenCalledWith(
        'credit-check-completed',
        expect.not.objectContaining({
          reference: expect.anything() as unknown,
        }),
      );
      expect(mockHandle.signal).toHaveBeenCalledWith(
        'credit-check-completed',
        expect.objectContaining({
          applicationId: 'app-123',
          result: 'rejected',
        }),
      );
    });

    it('throws NotFoundException when the workflow is not running', async () => {
      mockHandle.describe.mockRejectedValue(new Error('not found'));

      await expect(
        service.completeCreditCheck('app-123', 'approved'),
      ).rejects.toThrow(NotFoundException);
    });
  });
});
