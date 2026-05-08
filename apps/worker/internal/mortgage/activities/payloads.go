package activities

import "time"

type IntakeInput struct {
	ApplicationID string `json:"applicationId"`
	ApplicantName string `json:"applicantName"`
	// Scenario carries the demo scenario through to the activity so the
	// "applications started" metric can be labelled by scenario without the
	// workflow having to emit metrics directly. Empty when not supplied.
	Scenario string `json:"scenario,omitempty"`
}

type IntakeResult struct {
	ApplicationID string    `json:"applicationId"`
	ReceivedAt    time.Time `json:"receivedAt"`
}

type CreditCheckInput struct {
	ApplicationID              string `json:"applicationId"`
	ExternalFailureRatePercent int    `json:"externalFailureRatePercent,omitempty"`
}

// CreditCheckRequestResult is returned by RequestCreditCheck to confirm the
// request was dispatched and to provide a correlation reference.
type CreditCheckRequestResult struct {
	ApplicationID string `json:"applicationId"`
	Reference     string `json:"reference"`
}

type CreditCheckOutput struct {
	ApplicationID string    `json:"applicationId"`
	Result        string    `json:"result"`
	Reference     string    `json:"reference,omitempty"`
	CompletedAt   time.Time `json:"completedAt"`
}

// PropertyValuationInput carries the data required to value the property
// associated with an application. It is invoked only by the v2 mortgage
// workflow profile, between credit approval and offer reservation.
// PropertyValue is the operator-submitted value in pounds; the activity
// rejects non-positive values up front rather than accepting and silently
// passing through invalid input.
type PropertyValuationInput struct {
	ApplicationID              string  `json:"applicationId"`
	PropertyValue              float64 `json:"propertyValue"`
	ExternalFailureRatePercent int     `json:"externalFailureRatePercent,omitempty"`
}

// PropertyValuationResult is returned by PropertyValuation. The valuation ID
// is derived deterministically from the application ID so repeated invocations
// are idempotent. PropertyValue is echoed back so the audit trail can record
// the value that was actually used by the activity.
type PropertyValuationResult struct {
	ApplicationID string    `json:"applicationId"`
	ValuationID   string    `json:"valuationId"`
	PropertyValue float64   `json:"propertyValue"`
	ValuedAt      time.Time `json:"valuedAt"`
}

type ReserveOfferInput struct {
	ApplicationID              string `json:"applicationId"`
	ExternalFailureRatePercent int    `json:"externalFailureRatePercent,omitempty"`
}

type ReserveOfferResult struct {
	ApplicationID string    `json:"applicationId"`
	OfferID       string    `json:"offerId"`
	ReservedAt    time.Time `json:"reservedAt"`
}

type CompleteApplicationInput struct {
	ApplicationID string `json:"applicationId"`
	OfferID       string `json:"offerId"`
	// SimulateFailure causes the activity to fail on the first four attempts and
	// succeed on the fifth, demonstrating Temporal's automatic retry behaviour.
	// Used for the fail_after_offer_reservation demo scenario only.
	SimulateFailure            bool `json:"simulateFailure,omitempty"`
	ExternalFailureRatePercent int  `json:"externalFailureRatePercent,omitempty"`
}

type CompleteApplicationResult struct {
	ApplicationID string    `json:"applicationId"`
	CompletedAt   time.Time `json:"completedAt"`
}

type ReleaseOfferInput struct {
	ApplicationID string `json:"applicationId"`
	OfferID       string `json:"offerId"`
	// Scenario labels the "applications compensated" metric so demo runs can
	// be split by scenario in Prometheus. Empty when not supplied.
	Scenario                   string `json:"scenario,omitempty"`
	ExternalFailureRatePercent int    `json:"externalFailureRatePercent,omitempty"`
}

type ReleaseOfferResult struct {
	ApplicationID string    `json:"applicationId"`
	ReleasedAt    time.Time `json:"releasedAt"`
}

// SendNotificationInput carries the data required to dispatch the final
// applicant notification. ApplicationID identifies the recipient; Status is
// the terminal outcome of the workflow ("approved" or "rejected").
// Compensated outcomes do not produce a notification so are not represented
// here.
type SendNotificationInput struct {
	ApplicationID string `json:"applicationId"`
	Status        string `json:"status"`
	// Scenario labels the "applications completed" metric so demo runs can
	// be split by scenario in Prometheus. Empty when not supplied.
	Scenario string `json:"scenario,omitempty"`
	// SubmittedAt is the workflow-recorded application start time (intake).
	// The activity uses it once, alongside the completion counter, to
	// observe end-to-end application duration. Optional: when zero, the
	// duration histogram is not emitted for that execution.
	SubmittedAt                time.Time `json:"submittedAt,omitempty"`
	ExternalFailureRatePercent int       `json:"externalFailureRatePercent,omitempty"`
}

type SendNotificationResult struct {
	ApplicationID string    `json:"applicationId"`
	Status        string    `json:"status"`
	DeliveredAt   time.Time `json:"deliveredAt"`
}
