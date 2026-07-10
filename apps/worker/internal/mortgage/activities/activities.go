package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/temporal-sa/mortgage-application-demo/apps/worker/internal/observability"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// Activities groups all mortgage application activity implementations.
// Construct via NewActivities and register the returned pointer as a single
// unit on the worker. The struct itself carries no behaviour beyond holding
// the immutable worker profile injected at startup, so callers should never
// build it via composite literal directly.
type Activities struct {
	// workerProfile is the validated worker profile ("v1" or "v2") for the
	// process this activity registration belongs to. It is set once by
	// NewActivities and never mutated thereafter, so reading it from any
	// activity method is safe under Temporal's concurrent execution model.
	// It is unexported so tests and other packages must go through the
	// constructor and cannot accidentally produce an Activities with an
	// invalid profile.
	workerProfile string
}

// NewActivities constructs an Activities ready for worker registration.
// The worker profile is validated up front so a misconfigured worker fails
// fast at startup rather than silently emitting metrics with a wrong or
// empty version label. Only "v1" and "v2" are accepted; any other value is
// rejected with an error the caller is expected to surface as fatal.
func NewActivities(workerProfile string) (*Activities, error) {
	if workerProfile != "v1" && workerProfile != "v2" {
		return nil, fmt.Errorf("invalid worker profile %q", workerProfile)
	}

	return &Activities{
		workerProfile: workerProfile,
	}, nil
}

// workerVersionLabel returns the version label used for business metrics.
// Returns "unknown" when the receiver is nil or the profile is empty so an
// unset profile is visible in Prometheus rather than collapsing into the
// empty-string label. The nil-safe path keeps method-value references
// formed from a zero-value pointer (used by some test helpers and by
// workflow code that only needs a method-name handle) safe to evaluate.
func (a *Activities) workerVersionLabel() string {
	if a == nil || a.workerProfile == "" {
		return "unknown"
	}
	return a.workerProfile
}

// Intake validates and records the receipt of a mortgage application.
func (a *Activities) Intake(ctx context.Context, input IntakeInput) (IntakeResult, error) {
	logger := activity.GetLogger(ctx)
	d := randomActivityDelay()
	logger.Info("simulating activity delay", "activity", "Intake", "delay", d)
	time.Sleep(d)

	if input.ApplicationID == "" {
		return IntakeResult{}, fmt.Errorf("intake failed: applicationId is required")
	}

	if input.ApplicantName == "" {
		return IntakeResult{}, fmt.Errorf("intake failed: applicantName is required")
	}

	// Business metric: increment once per successful intake. Intake is the
	// first authoritative activity for a workflow, runs once per execution
	// and only after validation has passed, so this is a safe place to count
	// "applications started" without risking duplicates.
	observability.ApplicationsStartedTotal.
		WithLabelValues(input.Scenario, a.workerVersionLabel()).
		Inc()

	return IntakeResult{
		ApplicationID: input.ApplicationID,
		ReceivedAt:    time.Now(),
	}, nil
}

// RequestCreditCheck submits a credit and AML check request to the external bureau.
// This activity only dispatches the request. The result is delivered asynchronously
// via the credit-check-completed signal sent through the API.
func (a *Activities) RequestCreditCheck(ctx context.Context, input CreditCheckInput) (CreditCheckRequestResult, error) {
	logger := activity.GetLogger(ctx)
	d := randomActivityDelay()
	logger.Info("simulating activity delay", "activity", "RequestCreditCheck", "delay", d)
	time.Sleep(d)

	if err := maybeFailExternalDependency("RequestCreditCheck", input.ExternalFailureRatePercent); err != nil {
		logger.Warn("simulating external dependency failure", "activity", "RequestCreditCheck", "failureRatePercent", input.ExternalFailureRatePercent)
		return CreditCheckRequestResult{}, err
	}

	reference := "CREDIT-REQ-" + input.ApplicationID

	logger.Info(
		"credit check requested; awaiting external result via signal",
		"applicationId", input.ApplicationID,
		"reference", reference,
	)

	return CreditCheckRequestResult{
		ApplicationID: input.ApplicationID,
		Reference:     reference,
	}, nil
}

// PropertyValuation simulates an external property valuation step.
// It is only invoked by the v2 mortgage workflow profile, between credit
// approval and offer reservation. The valuation ID is derived deterministically
// from the application ID, making this activity idempotent under retry.
func (a *Activities) PropertyValuation(ctx context.Context, input PropertyValuationInput) (PropertyValuationResult, error) {
	logger := activity.GetLogger(ctx)
	d := randomActivityDelay()
	logger.Info("simulating activity delay", "activity", "PropertyValuation", "delay", d)
	time.Sleep(d)

	if err := maybeFailExternalDependency("PropertyValuation", input.ExternalFailureRatePercent); err != nil {
		logger.Warn("simulating external dependency failure", "activity", "PropertyValuation", "failureRatePercent", input.ExternalFailureRatePercent)
		return PropertyValuationResult{}, err
	}

	if input.ApplicationID == "" {
		return PropertyValuationResult{}, fmt.Errorf("property valuation failed: applicationId is required")
	}

	// A non-positive property value is treated as a wiring bug rather than a
	// transient external failure: a zero or negative valuation should never
	// have been signalled into the workflow, so retrying would not help.
	if input.PropertyValue <= 0 {
		return PropertyValuationResult{}, temporal.NewNonRetryableApplicationError(
			"property valuation failed: propertyValue must be positive",
			"InvalidPropertyValuation",
			nil,
		)
	}

	valuationID := "VAL-" + input.ApplicationID

	logger.Info(
		"property valuation completed",
		"applicationId", input.ApplicationID,
		"valuationId", valuationID,
		"propertyValue", input.PropertyValue,
	)

	return PropertyValuationResult{
		ApplicationID: input.ApplicationID,
		ValuationID:   valuationID,
		PropertyValue: input.PropertyValue,
		ValuedAt:      time.Now(),
	}, nil
}

// ReserveOffer allocates a mortgage offer for an approved application.
// The offer ID is derived deterministically from the application ID, making
// this activity idempotent: repeated calls for the same application always
// return the same offer. This also makes compensation straightforward: the
// offer ID is stable and can be passed directly to ReleaseOffer.
func (a *Activities) ReserveOffer(ctx context.Context, input ReserveOfferInput) (ReserveOfferResult, error) {
	logger := activity.GetLogger(ctx)
	d := randomActivityDelay()
	logger.Info("simulating activity delay", "activity", "ReserveOffer", "delay", d)
	time.Sleep(d)

	if err := maybeFailExternalDependency("ReserveOffer", input.ExternalFailureRatePercent); err != nil {
		logger.Warn("simulating external dependency failure", "activity", "ReserveOffer", "failureRatePercent", input.ExternalFailureRatePercent)
		return ReserveOfferResult{}, err
	}

	offerID := "OFFER-" + input.ApplicationID

	logger.Info(
		"offer reserved",
		"applicationId", input.ApplicationID,
		"offerId", offerID,
	)

	return ReserveOfferResult{
		ApplicationID: input.ApplicationID,
		OfferID:       offerID,
		ReservedAt:    time.Now(),
	}, nil
}

// ReleaseOffer cancels an existing offer reservation.
// This is the compensating action for ReserveOffer.
func (a *Activities) ReleaseOffer(ctx context.Context, input ReleaseOfferInput) (ReleaseOfferResult, error) {
	logger := activity.GetLogger(ctx)
	d := randomActivityDelay()
	logger.Info("simulating activity delay", "activity", "ReleaseOffer", "delay", d)
	time.Sleep(d)

	if err := maybeFailExternalDependency("ReleaseOffer", input.ExternalFailureRatePercent); err != nil {
		logger.Warn("simulating external dependency failure", "activity", "ReleaseOffer", "failureRatePercent", input.ExternalFailureRatePercent)
		return ReleaseOfferResult{}, err
	}

	logger.Info(
		"offer released",
		"applicationId", input.ApplicationID,
		"offerId", input.OfferID,
	)

	// Business metric: increment once per successful compensation. ReleaseOffer
	// is the workflow's only compensating activity so a successful run here
	// uniquely corresponds to a compensated application.
	observability.ApplicationsCompensatedTotal.
		WithLabelValues(input.Scenario, a.workerVersionLabel()).
		Inc()

	return ReleaseOfferResult{
		ApplicationID: input.ApplicationID,
		ReleasedAt:    time.Now(),
	}, nil
}

// SendNotification simulates dispatching the final applicant notification
// (e.g. an email, push or letter) once the workflow reaches a terminal
// business outcome. It is intentionally a small simulation: the activity
// honours the same random delay and failure injection patterns as the other
// demo activities so it can be observed in the Temporal UI alongside them.
//
// The activity validates that an applicationId and status are present. Both
// are required to produce a meaningful notification; an empty value indicates
// a wiring bug rather than a transient external failure, so the resulting
// error is non-retryable.
func (a *Activities) SendNotification(ctx context.Context, input SendNotificationInput) (SendNotificationResult, error) {
	logger := activity.GetLogger(ctx)
	d := randomActivityDelay()
	logger.Info("simulating activity delay", "activity", "SendNotification", "delay", d)
	time.Sleep(d)

	if err := maybeFailExternalDependency("SendNotification", input.ExternalFailureRatePercent); err != nil {
		logger.Warn("simulating external dependency failure", "activity", "SendNotification", "failureRatePercent", input.ExternalFailureRatePercent)
		return SendNotificationResult{}, err
	}

	if input.ApplicationID == "" {
		return SendNotificationResult{}, temporal.NewNonRetryableApplicationError(
			"send notification failed: applicationId is required",
			"InvalidNotificationInput",
			nil,
		)
	}
	if input.Status == "" {
		return SendNotificationResult{}, temporal.NewNonRetryableApplicationError(
			"send notification failed: status is required",
			"InvalidNotificationInput",
			nil,
		)
	}

	logger.Info(
		"notification dispatched to applicant",
		"applicationId", input.ApplicationID,
		"status", input.Status,
	)

	// Business metric: increment once per terminal applicant notification.
	// SendNotification runs only at the approved or rejected terminal outcomes
	// and never on the compensation path, so the Status value is the workflow
	// outcome label.
	observability.ApplicationsCompletedTotal.
		WithLabelValues(input.Scenario, a.workerVersionLabel(), input.Status).
		Inc()

	// Business metric: end-to-end application duration. Observed at the same
	// terminal point as the completion counter, so it fires exactly once per
	// successfully notified workflow execution. Activity bodies do not run
	// during workflow replay, and earlier failed attempts return before
	// reaching this point, so no duplicate observations are produced. Skipped
	// when SubmittedAt is unset (e.g. legacy callers in tests) or when the
	// computed duration is non-positive, so the histogram never records a
	// degenerate sample.
	if !input.SubmittedAt.IsZero() {
		if duration := time.Since(input.SubmittedAt); duration > 0 {
			observability.ApplicationDurationSeconds.
				WithLabelValues(input.Scenario, a.workerVersionLabel(), input.Status).
				Observe(duration.Seconds())
		}
	}

	return SendNotificationResult{
		ApplicationID: input.ApplicationID,
		Status:        input.Status,
		DeliveredAt:   time.Now(),
	}, nil
}

// CompleteApplication finalises the mortgage once an offer has been reserved.
//
// When SimulateFailure is set the activity fails on the first four attempts and
// succeeds on the fifth, demonstrating Temporal's automatic retry behaviour. Each
// failure is a retryable ApplicationError so Temporal drives the backoff — no manual
// retry loop is needed in workflow code.
func (a *Activities) CompleteApplication(ctx context.Context, input CompleteApplicationInput) (CompleteApplicationResult, error) {
	logger := activity.GetLogger(ctx)
	info := activity.GetInfo(ctx)
	d := randomActivityDelay()
	logger.Info("simulating activity delay", "activity", "CompleteApplication", "delay", d)
	time.Sleep(d)

	if err := maybeFailExternalDependency("CompleteApplication", input.ExternalFailureRatePercent); err != nil {
		logger.Warn("simulating external dependency failure", "activity", "CompleteApplication", "failureRatePercent", input.ExternalFailureRatePercent)
		return CompleteApplicationResult{}, err
	}

	if input.SimulateFailure && info.Attempt <= 4 {
		logger.Warn(
			"simulating completion failure for demo; Temporal will retry",
			"applicationId", input.ApplicationID,
			"offerId", input.OfferID,
			"attempt", info.Attempt,
		)
		return CompleteApplicationResult{}, temporal.NewApplicationError(
			"completion failure injected for demo",
			"InjectedFulfilmentFailure",
			nil,
		)
	}

	logger.Info(
		"mortgage application completed",
		"applicationId", input.ApplicationID,
		"offerId", input.OfferID,
		"attempt", info.Attempt,
	)

	return CompleteApplicationResult{
		ApplicationID: input.ApplicationID,
		CompletedAt:   time.Now(),
	}, nil
}
