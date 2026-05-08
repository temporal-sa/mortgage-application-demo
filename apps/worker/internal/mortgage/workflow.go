package mortgage

import (
	"strconv"
	"time"

	saga "github.com/mrsimonemms/golang-helpers/temporal"
	"github.com/temporal-sa/mortgage-application-demo/apps/worker/internal/mortgage/activities"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// WorkflowTypeName is the name under which the mortgage workflow is registered
// with the worker. Both v1 and v2 worker profiles register their respective
// implementations under this single name. Worker Deployment Versioning is
// responsible for routing executions to the worker that started them (v1) or
// to the current Worker Deployment Version for new executions (v2 once
// promoted). Patch-style versioning (workflow.GetVersion) is intentionally not
// used in this PoC.
const WorkflowTypeName = "MortgageApplicationWorkflow"

// MortgageApplicationWorkflow is the v1 mortgage workflow. It runs the
// original flow without a property valuation step. v1 worker profiles register
// this implementation under WorkflowTypeName so executions started before the
// v2 deployment was promoted continue to run on v1 workers safely.
func MortgageApplicationWorkflow(ctx workflow.Context, event MortgageApplicationSubmitted) (MortgageApplication, error) {
	return runMortgageApplication(ctx, event, false)
}

// MortgageApplicationWorkflowV2 is the v2 mortgage workflow. It is identical
// to v1 except that a Property Valuation step runs after a successful credit
// check and before offer reservation. v2 worker profiles register this
// implementation under WorkflowTypeName so new executions started after v2 is
// promoted as the current Worker Deployment Version run the v2 flow.
func MortgageApplicationWorkflowV2(ctx workflow.Context, event MortgageApplicationSubmitted) (MortgageApplication, error) {
	return runMortgageApplication(ctx, event, true)
}

// runMortgageApplication is the shared workflow body used by both v1 and v2
// worker profiles. The propertyValuationEnabled flag is supplied at registration
// time via the entrypoint (MortgageApplicationWorkflow or
// MortgageApplicationWorkflowV2) so workflow code never reads non-deterministic
// configuration at runtime.
//
// Steps (v1 and v2):
//  1. Intake — record receipt of the application
//  2. Credit check request — dispatch to external bureau (activity)
//  3. Durable wait — block until CreditCheckCompleted signal arrives (signal)
//  4. Property valuation — only when propertyValuationEnabled is true (v2)
//  5. Offer reservation — allocate a mortgage offer
//  6. Complete application — mark the application as completed
//
// Saga pattern: a compensation function is registered immediately after the offer
// reservation succeeds. If any later step fails and the workflow returns an error,
// the deferred compensator releases the reserved offer from a disconnected context
// and updates the audit trail. The workflow still returns the original error so the
// business failure is correctly reflected in Temporal.
func runMortgageApplication(ctx workflow.Context, event MortgageApplicationSubmitted, propertyValuationEnabled bool) (app MortgageApplication, err error) {
	app = MortgageApplication{
		ApplicationID: event.ApplicationID,
		ApplicantName: event.ApplicantName,
		Status:        StatusSubmitted,
		CurrentStep:   "submitted",
		CreatedAt:     workflow.Now(ctx),
		UpdatedAt:     workflow.Now(ctx),
		Timeline:      []TimelineEntry{},
	}

	// The query handler returns a snapshot with an independent copy of the
	// timeline so callers cannot observe future mutations to the workflow's
	// slice. While the workflow is waiting on an async dependency
	// (PendingDependency is set) SLABreached is computed live against the
	// current deadline so callers see a fresh status without the workflow
	// needing to be unblocked. Once the wait resolves PendingDependency is
	// cleared and SLAStatus / SLABreached are read from the durable values
	// persisted by the wait function.
	if err = workflow.SetQueryHandler(ctx, QueryApplication, func() (MortgageApplication, error) {
		snapshot := app
		snapshot.Timeline = append([]TimelineEntry(nil), app.Timeline...)
		if snapshot.PendingDependency != nil && snapshot.SLADeadline != nil {
			breached := workflow.Now(ctx).After(*snapshot.SLADeadline)
			snapshot.SLABreached = &breached
		}
		return snapshot, nil
	}); err != nil {
		return
	}

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
	})

	// acts is used purely as a method-name handle for workflow.ExecuteActivity;
	// the receiver carries no behaviour during workflow code and is never
	// invoked through this reference. The actual activity implementation is
	// constructed via activities.NewActivities and registered with the worker
	// in main.go, so a zero-value pointer here keeps workflow code free of
	// profile selection while still satisfying the pointer-receiver method
	// set required by the underlying activity definitions.
	acts := &activities.Activities{}
	failureRate := event.ExternalFailureRatePercent

	// Compensation runs in LIFO order from a disconnected context whenever the
	// workflow is returning a non-nil error and at least one step registered a
	// compensation. The disconnected context ensures compensation activities are
	// not cancelled along with the failing workflow.
	var comp saga.Compensator
	defer func() {
		if err != nil {
			disconnectedCtx, _ := workflow.NewDisconnectedContext(ctx)
			comp.Compensate(disconnectedCtx)
		}
	}()

	recordTimeline(&app, ctx, "submitted", TimelineCompleted, "Application received", map[string]string{
		"applicationId": app.ApplicationID,
		"applicantName": app.ApplicantName,
	})

	if event.OriginalApplicationID != "" {
		recordTimeline(&app, ctx, "operator_rerun_application", TimelineCompleted,
			"Application re-run by operator",
			map[string]string{"originalApplicationId": event.OriginalApplicationID})
	}

	upsertSearchAttributes(ctx, &app, false)

	if err = runIntake(ctx, actCtx, &app, acts, event.Scenario); err != nil {
		return
	}

	var creditResult CreditCheckCompleted
	if creditResult, err = requestAndWaitCreditCheck(ctx, actCtx, &app, acts, failureRate); err != nil {
		return
	}

	if creditResult.Result == CreditCheckRejected {
		app.Status = StatusRejected
		app.CurrentStep = "rejected"
		meta := map[string]string{"result": string(creditResult.Result)}
		if creditResult.Reference != "" {
			meta["reference"] = creditResult.Reference
		}
		recordTimeline(&app, ctx, "credit_check", TimelineCompleted, "Credit check rejected", meta)
		upsertSearchAttributes(ctx, &app, false)
		runSendNotification(ctx, &app, acts, NotificationRejected, event.Scenario, failureRate)
		return // err is nil; deferred comp is a no-op
	}

	meta := map[string]string{"result": string(creditResult.Result)}
	if creditResult.Reference != "" {
		meta["reference"] = creditResult.Reference
	}
	recordTimeline(&app, ctx, "credit_check", TimelineCompleted, "Credit check approved", meta)

	if propertyValuationEnabled {
		if err = runPropertyValuation(ctx, actCtx, &app, acts, failureRate); err != nil {
			return
		}
	}

	if err = runOfferReservation(ctx, actCtx, &app, acts, failureRate); err != nil {
		return
	}

	// Register compensation immediately after offer reservation succeeds.
	// Inputs are captured by value now so the closure does not read app fields
	// that may be mutated later (e.g. OfferID is cleared after compensation).
	registeredAppID := app.ApplicationID
	registeredOfferID := app.OfferID
	comp.Add(func(compCtx workflow.Context) error {
		return compensateReleaseOffer(compCtx, &app, acts, registeredAppID, registeredOfferID, event.Scenario, failureRate)
	})

	if err = runCompleteApplication(ctx, &app, acts, event.Scenario, failureRate); err != nil {
		// Record the failure before the deferred compensator runs so the audit
		// trail shows the fulfilment failure ahead of the compensation entries.
		recordTimeline(&app, ctx, "fulfilment", TimelineFailed,
			"Fulfilment failed after maximum retries",
			map[string]string{
				"offerId": registeredOfferID,
				"reason":  "Maximum retry attempts exhausted",
			})
		app.Status = StatusCompensationRequired
		app.CurrentStep = "compensation"
		upsertSearchAttributes(ctx, &app, false)
		return // deferred compensator handles the release-offer step
	}

	// Approved terminal outcome: notify the applicant. Notification failures
	// are logged and recorded in the audit trail but never propagated, so a
	// downstream notification problem cannot retroactively trigger
	// compensation of an already-fulfilled mortgage.
	runSendNotification(ctx, &app, acts, NotificationApproved, event.Scenario, failureRate)

	return
}

func runIntake(ctx, actCtx workflow.Context, app *MortgageApplication, acts *activities.Activities, scenario WorkflowScenario) error {
	app.CurrentStep = "intake"
	upsertSearchAttributes(ctx, app, false)
	recordTimeline(app, ctx, "intake", TimelineStarted, "Application intake started")

	var result activities.IntakeResult
	if err := workflow.ExecuteActivity(actCtx, acts.Intake, activities.IntakeInput{
		ApplicationID: app.ApplicationID,
		ApplicantName: app.ApplicantName,
		Scenario:      string(scenario),
	}).Get(ctx, &result); err != nil {
		return err
	}

	recordTimeline(app, ctx, "intake", TimelineCompleted, "Application intake recorded")

	return nil
}

func requestCreditCheck(ctx, actCtx workflow.Context, app *MortgageApplication, acts *activities.Activities, failureRate int) error {
	app.Status = StatusCreditCheckPending
	app.CurrentStep = "credit_check"
	upsertSearchAttributes(ctx, app, false)

	var result activities.CreditCheckRequestResult
	if err := workflow.ExecuteActivity(actCtx, acts.RequestCreditCheck, activities.CreditCheckInput{
		ApplicationID:              app.ApplicationID,
		ExternalFailureRatePercent: failureRate,
	}).Get(ctx, &result); err != nil {
		return err
	}

	recordTimeline(app, ctx, "credit_check", TimelineStarted, "Credit and AML check requested", map[string]string{
		"reference": result.Reference,
	})

	return nil
}

// requestAndWaitCreditCheck requests a credit check and waits for the result. If the
// operator sends a RetryCreditCheckSignal while the workflow is waiting, the credit
// check is re-requested and the wait restarts. This loop continues until a result
// arrives. Each operator retry records an operator_retry_credit_check audit entry.
func requestAndWaitCreditCheck(ctx, actCtx workflow.Context, app *MortgageApplication, acts *activities.Activities, failureRate int) (CreditCheckCompleted, error) {
	for {
		if err := requestCreditCheck(ctx, actCtx, app, acts, failureRate); err != nil {
			return CreditCheckCompleted{}, err
		}

		result, retried := waitForCreditResultOrRetry(ctx, app)
		if !retried {
			return result, nil
		}

		recordTimeline(app, ctx, "operator_retry_credit_check", TimelineCompleted,
			"Operator requested credit check retry",
			map[string]string{"applicationId": app.ApplicationID})
	}
}

// waitForCreditResultOrRetry blocks the workflow durably until either the
// CreditCheckCompleted signal or the RetryCreditCheckSignal arrives. Returns the
// credit result and retried=false on a normal result, or zero value and retried=true
// when the operator requests a retry. AwaitingExternalSignal is set while blocked.
//
// Pending dependency, pending-since and SLA deadline are recorded on entry and
// cleared on exit so the query handler exposes accurate SLA visibility while
// the workflow is waiting on an external signal, and stale transient data is
// not surfaced once the wait resolves.
//
// On a successful credit completion the durable SLA outcome (SLAStatus and
// SLABreached) is captured before the transient deadline is cleared, and the
// WithinSLA search attribute is updated. Operator retries reset SLA tracking
// for the next attempt: only the final attempt's outcome is persisted.
func waitForCreditResultOrRetry(ctx workflow.Context, app *MortgageApplication) (CreditCheckCompleted, bool) {
	app.CurrentStep = "awaiting_credit_result"

	pendingDep := PendingCreditCheck
	pendingSince := workflow.Now(ctx)
	slaDeadline := pendingSince.Add(CreditCheckSLA)
	app.PendingDependency = &pendingDep
	app.PendingSince = &pendingSince
	app.SLADeadline = &slaDeadline
	// Reset any SLA outcome persisted by a previous attempt so the query
	// handler's live computation is the source of truth during this wait.
	// Only the final attempt's outcome is retained.
	app.SLAStatus = nil
	app.SLABreached = nil

	recordTimeline(app, ctx, "credit_check", TimelineWaiting, "Awaiting credit bureau result")
	upsertSearchAttributes(ctx, app, true)

	creditCheckCh := workflow.GetSignalChannel(ctx, CreditCheckCompletedSignal)
	retryCh := workflow.GetSignalChannel(ctx, RetryCreditCheckSignal)

	var result CreditCheckCompleted
	var retried bool

	workflow.NewSelector(ctx).
		AddReceive(creditCheckCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, &result)
		}).
		AddReceive(retryCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, nil)
			retried = true
		}).
		Select(ctx)

	if retried {
		// Operator retry: drop the in-flight deadline so the next iteration's
		// fresh deadline is the only one in scope. The persistent outcome
		// fields were already nil while this wait was running.
		app.SLADeadline = nil
	} else {
		// Capture the final SLA outcome durably. SLADeadline is intentionally
		// retained so the UI and audit trail can continue to show against
		// which deadline the wait was evaluated.
		breached := workflow.Now(ctx).After(slaDeadline)
		status := SLAStatusWithin
		if breached {
			status = SLAStatusBreached
		}
		app.SLAStatus = &status
		app.SLABreached = &breached
	}

	app.PendingDependency = nil
	app.PendingSince = nil

	upsertSearchAttributes(ctx, app, false)

	return result, retried
}

// runPropertyValuation executes the property valuation step. It is invoked only
// by the v2 workflow profile, after a successful credit and AML check and
// before offer reservation.
//
// The step has three phases, all recorded on the audit trail:
//  1. property_valuation/requested — the workflow records that an operator
//     property value is required and durably waits on the
//     PropertyValuationSubmittedSignal. While waiting, PendingDependency is
//     set so the UI can show the "Submit Property Valuation" action.
//  2. property_valuation/submitted — the operator's value has arrived and
//     been recorded on the application state.
//  3. property_valuation/started → property_valuation/completed (or /failed)
//     — the activity runs against the submitted value with the standard
//     retry/failure semantics (Temporal drives retries; a final failure
//     surfaces as property_valuation/failed and the workflow returns the
//     error). No offer has been reserved at this point so no compensation is
//     required.
func runPropertyValuation(ctx, actCtx workflow.Context, app *MortgageApplication, acts *activities.Activities, failureRate int) error {
	propertyValue := waitForPropertyValuation(ctx, app)

	// Deliberate defect for the "bad deployment" demo scenario. Living in
	// the v2 workflow path means v1 executions are unaffected; the trigger
	// is a specific property value so an operator can opt in or out. The
	// panic surfaces as a workflow task failure, which Temporal will keep
	// retrying against the current Worker Deployment Version until the
	// defect is resolved by rolling back to v1 or by deploying a fixed v2.
	if propertyValue == 350001 {
		panic("simulated defect in valuation workflow logic")
	}

	app.CurrentStep = "property_valuation"
	upsertSearchAttributes(ctx, app, false)
	recordTimeline(app, ctx, "property_valuation", TimelineStarted,
		"Property valuation started",
		map[string]string{
			"applicationId": app.ApplicationID,
			"propertyValue": formatPropertyValue(propertyValue),
		})

	var result activities.PropertyValuationResult
	if err := workflow.ExecuteActivity(actCtx, acts.PropertyValuation, activities.PropertyValuationInput{
		ApplicationID:              app.ApplicationID,
		PropertyValue:              propertyValue,
		ExternalFailureRatePercent: failureRate,
	}).Get(ctx, &result); err != nil {
		recordTimeline(app, ctx, "property_valuation", TimelineFailed,
			"Property valuation failed",
			map[string]string{
				"applicationId": app.ApplicationID,
				"propertyValue": formatPropertyValue(propertyValue),
			})
		return err
	}

	recordTimeline(app, ctx, "property_valuation", TimelineCompleted,
		"Property valuation completed",
		map[string]string{
			"applicationId": app.ApplicationID,
			"valuationId":   result.ValuationID,
			"propertyValue": formatPropertyValue(propertyValue),
		})
	upsertSearchAttributes(ctx, app, false)

	return nil
}

// waitForPropertyValuation blocks the workflow durably until the operator
// submits a property value via PropertyValuationSubmittedSignal. While
// blocked PendingDependency is set so the UI can show the "Submit Property
// Valuation" action; both the dependency and PendingSince are cleared once
// a positive value arrives.
//
// Non-positive submissions (e.g. an operator typo) are rejected at the wait
// boundary and the workflow keeps waiting for a valid value. This keeps
// invariants simple downstream: the activity only ever sees positive values.
func waitForPropertyValuation(ctx workflow.Context, app *MortgageApplication) float64 {
	app.CurrentStep = "awaiting_property_valuation"

	pendingDep := PendingPropertyValuation
	pendingSince := workflow.Now(ctx)
	app.PendingDependency = &pendingDep
	app.PendingSince = &pendingSince

	recordTimeline(app, ctx, "property_valuation", TimelineWaiting,
		"Awaiting operator property valuation",
		map[string]string{"applicationId": app.ApplicationID})
	upsertSearchAttributes(ctx, app, true)

	ch := workflow.GetSignalChannel(ctx, PropertyValuationSubmittedSignal)
	var submitted PropertyValuationSubmitted
	for {
		ch.Receive(ctx, &submitted)
		if submitted.PropertyValue > 0 {
			break
		}
		// Ignore non-positive submissions: keep waiting for a valid value
		// rather than corrupting downstream state. The API rejects this case
		// up front, but the workflow remains defensive.
		workflow.GetLogger(ctx).Warn("ignoring non-positive property valuation; awaiting a valid value",
			"applicationId", app.ApplicationID,
			"propertyValue", submitted.PropertyValue,
		)
	}

	value := submitted.PropertyValue
	app.PropertyValue = &value
	app.PendingDependency = nil
	app.PendingSince = nil

	// The operator submission is recorded under a distinct step name so the
	// /completed entry on the property_valuation step itself remains owned
	// by the activity success path.
	recordTimeline(app, ctx, "property_valuation_submitted", TimelineCompleted,
		"Operator submitted property valuation",
		map[string]string{
			"applicationId": app.ApplicationID,
			"propertyValue": formatPropertyValue(value),
		})
	upsertSearchAttributes(ctx, app, false)

	return value
}

// formatPropertyValue renders a property value as a plain numeric string for
// audit metadata. Audit metadata is map[string]string by contract; a numeric
// formatter keeps the trail self-consistent and avoids locale surprises.
func formatPropertyValue(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func runOfferReservation(ctx, actCtx workflow.Context, app *MortgageApplication, acts *activities.Activities, failureRate int) error {
	app.CurrentStep = "offer_reservation"
	recordTimeline(app, ctx, "offer_reservation", TimelineStarted, "Offer reservation started")
	upsertSearchAttributes(ctx, app, false)

	var result activities.ReserveOfferResult
	if err := workflow.ExecuteActivity(actCtx, acts.ReserveOffer, activities.ReserveOfferInput{
		ApplicationID:              app.ApplicationID,
		ExternalFailureRatePercent: failureRate,
	}).Get(ctx, &result); err != nil {
		return err
	}

	app.OfferID = result.OfferID
	app.Status = StatusOfferReserved
	recordTimeline(app, ctx, "offer_reservation", TimelineCompleted, "Offer reserved", map[string]string{
		"offerId": result.OfferID,
	})
	upsertSearchAttributes(ctx, app, false)

	return nil
}

// runCompleteApplication executes the fulfilment step with scenario-specific retry
// behaviour. The retry policy and SimulateFailure flag are scoped to this step only
// so they do not affect any other activity in the workflow.
//
// fail_after_offer_reservation: fails on attempts 1–4, succeeds on attempt 5.
// fail_and_compensate_after_offer_reservation: fails on all 3 attempts, exhausting
// the retry policy. The caller is responsible for triggering compensation.
func runCompleteApplication(ctx workflow.Context, app *MortgageApplication, acts *activities.Activities, scenario WorkflowScenario, failureRate int) error {
	retryPolicy := &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    10 * time.Second,
		MaximumAttempts:    5,
	}
	simulateFailure := false

	switch scenario {
	case ScenarioFailAfterOfferReservation:
		// Fails on attempts 1–4; succeeds on attempt 5.
		simulateFailure = true
	case ScenarioFailAndCompensate:
		// Fails on all 3 attempts, surfacing an error to the workflow.
		simulateFailure = true
		retryPolicy.MaximumAttempts = 3
	}

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         retryPolicy,
	})

	app.CurrentStep = "fulfilment"
	recordTimeline(app, ctx, "fulfilment", TimelineStarted, "Fulfilment started", map[string]string{
		"offerId": app.OfferID,
	})
	upsertSearchAttributes(ctx, app, false)

	var result activities.CompleteApplicationResult
	if err := workflow.ExecuteActivity(actCtx, acts.CompleteApplication, activities.CompleteApplicationInput{
		ApplicationID:              app.ApplicationID,
		OfferID:                    app.OfferID,
		SimulateFailure:            simulateFailure,
		ExternalFailureRatePercent: failureRate,
	}).Get(ctx, &result); err != nil {
		return err
	}

	app.Status = StatusCompleted
	app.CurrentStep = "completed"
	recordTimeline(app, ctx, "fulfilment", TimelineCompleted, "Mortgage application completed", map[string]string{
		"offerId": app.OfferID,
		"status":  string(StatusCompleted),
	})
	upsertSearchAttributes(ctx, app, false)

	return nil
}

// compensateReleaseOffer is the compensation action for a successful offer reservation.
// It runs from a disconnected context so it is not cancelled when the parent workflow
// context is cancelled on failure. applicationID and offerID are passed explicitly
// rather than read from app to ensure the correct values are used even if app is
// mutated between registration and execution.
func compensateReleaseOffer(ctx workflow.Context, app *MortgageApplication, acts *activities.Activities, applicationID, offerID string, scenario WorkflowScenario, failureRate int) error {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
	})

	recordTimeline(app, ctx, "compensation", TimelineStarted,
		"Initiating compensation: releasing reserved offer",
		map[string]string{"offerId": offerID})

	var result activities.ReleaseOfferResult
	if err := workflow.ExecuteActivity(actCtx, acts.ReleaseOffer, activities.ReleaseOfferInput{
		ApplicationID:              applicationID,
		OfferID:                    offerID,
		Scenario:                   string(scenario),
		ExternalFailureRatePercent: failureRate,
	}).Get(ctx, &result); err != nil {
		return err
	}

	// Clear the offer ID: the offer is no longer reserved.
	app.OfferID = ""
	app.Status = StatusCompensated
	app.CurrentStep = "compensated"
	recordTimeline(app, ctx, "compensation", TimelineCompleted,
		"Compensation complete: offer released",
		map[string]string{"offerId": offerID, "status": string(StatusCompensated)})
	upsertSearchAttributes(ctx, app, false)

	return nil
}

// runSendNotification dispatches the final applicant notification through the
// SendNotification activity. It is invoked at terminal business outcomes
// (approved or rejected) but never on the saga compensation path so that
// rolled-back applications produce no applicant-facing message.
//
// Temporal drives a small retry policy on the activity to absorb transient
// failures from the simulated external dependency. If retries are exhausted
// the workflow records a notification/failed audit entry and continues
// without propagating the error, so a notification dispatch problem cannot
// retroactively trigger compensation of an otherwise successful workflow.
func runSendNotification(ctx workflow.Context, app *MortgageApplication, acts *activities.Activities, status NotificationStatus, scenario WorkflowScenario, failureRate int) {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    10 * time.Second,
			MaximumAttempts:    3,
		},
	})

	recordTimeline(app, ctx, "notification", TimelineStarted,
		"Notifying applicant of final outcome",
		map[string]string{
			"applicationId": app.ApplicationID,
			"status":        string(status),
		})

	var result activities.SendNotificationResult
	if err := workflow.ExecuteActivity(actCtx, acts.SendNotification, activities.SendNotificationInput{
		ApplicationID:              app.ApplicationID,
		Status:                     string(status),
		Scenario:                   string(scenario),
		SubmittedAt:                app.CreatedAt,
		ExternalFailureRatePercent: failureRate,
	}).Get(ctx, &result); err != nil {
		workflow.GetLogger(ctx).Warn("notification dispatch failed; continuing without propagating",
			"applicationId", app.ApplicationID,
			"status", string(status),
			"error", err)
		recordTimeline(app, ctx, "notification", TimelineFailed,
			"Notification dispatch failed after retries; outcome unaffected",
			map[string]string{
				"applicationId": app.ApplicationID,
				"status":        string(status),
			})
		return
	}

	recordTimeline(app, ctx, "notification", TimelineCompleted,
		"Notification dispatched to applicant",
		map[string]string{
			"applicationId": app.ApplicationID,
			"status":        string(status),
		})
}

// recordTimeline appends an audit entry to the application timeline and advances
// UpdatedAt. The optional metadata map carries structured data for the entry.
func recordTimeline(app *MortgageApplication, ctx workflow.Context, step string, status TimelineStatus, details string, metadata ...map[string]string) {
	entry := TimelineEntry{
		Step:      step,
		Status:    status,
		Timestamp: workflow.Now(ctx),
		Details:   details,
	}
	if len(metadata) > 0 {
		entry.Metadata = metadata[0]
	}
	app.Timeline = append(app.Timeline, entry)
	app.UpdatedAt = workflow.Now(ctx)
}

// upsertSearchAttributes syncs the custom search attributes to current workflow
// state. awaitingSignal must be passed explicitly as it is not stored on
// MortgageApplication. WithinSLA is only included once the workflow has
// finalised an SLA outcome; before that the attribute is left unset.
// Failures are logged as warnings; they do not abort the workflow.
func upsertSearchAttributes(ctx workflow.Context, app *MortgageApplication, awaitingSignal bool) {
	updates := []temporal.SearchAttributeUpdate{
		saApplicationStatus.ValueSet(string(app.Status)),
		saCurrentStep.ValueSet(app.CurrentStep),
		saHasOffer.ValueSet(app.OfferID != ""),
		saAwaitingExternalSignal.ValueSet(awaitingSignal),
	}
	if app.SLABreached != nil {
		updates = append(updates, saWithinSLA.ValueSet(!*app.SLABreached))
	}
	if err := workflow.UpsertTypedSearchAttributes(ctx, updates...); err != nil {
		workflow.GetLogger(ctx).Warn("failed to upsert search attributes", "error", err)
	}
}
