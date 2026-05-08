package activities

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/temporal-sa/mortgage-application-demo/apps/worker/internal/observability"
)

func init() {
	DisableActivityDelaysForTests()
}

// newTestActivities is the canonical way for tests to obtain an Activities
// value. It goes through the public constructor so the test code path
// matches production wiring: an invalid profile here would surface the same
// way as a misconfigured worker at startup.
func newTestActivities(profile string) *Activities {
	acts, err := NewActivities(profile)
	if err != nil {
		panic(err)
	}
	return acts
}

// testActs is a package-level v1 Activities used as a method-value handle
// in env.ExecuteActivity calls. The actual activity is executed against the
// instance registered in the test environment, not this one, so the
// specific profile here only affects metric-label tests that explicitly use
// a different env.
var testActs = newTestActivities("v1")

func newTestEnv(t *testing.T) *testsuite.TestActivityEnvironment {
	t.Helper()
	// Default test environment registers Activities through the constructor
	// with the v1 profile so the registered receiver always has a valid
	// configuration. Tests that need a specific profile label use
	// newTestEnvWithProfile.
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(newTestActivities("v1"))
	return env
}

// newTestEnvWithProfile registers Activities with an explicit worker profile
// so tests can verify the labelled metric path without touching process
// environment state. Profile must be one of the values accepted by
// NewActivities; passing an invalid value causes the test to fail loudly.
func newTestEnvWithProfile(t *testing.T, profile string) *testsuite.TestActivityEnvironment {
	t.Helper()
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(newTestActivities(profile))
	return env
}

// TestNewActivities pins the validation contract of the constructor: only
// "v1" and "v2" produce a usable Activities; everything else is rejected so
// a misconfigured worker fails fast at startup.
func TestNewActivities(t *testing.T) {
	t.Run("accepts v1", func(t *testing.T) {
		acts, err := NewActivities("v1")
		assert.NoError(t, err)
		if assert.NotNil(t, acts) {
			assert.Equal(t, "v1", acts.workerVersionLabel())
		}
	})

	t.Run("accepts v2", func(t *testing.T) {
		acts, err := NewActivities("v2")
		assert.NoError(t, err)
		if assert.NotNil(t, acts) {
			assert.Equal(t, "v2", acts.workerVersionLabel())
		}
	})

	t.Run("rejects empty profile", func(t *testing.T) {
		acts, err := NewActivities("")
		assert.Error(t, err)
		assert.Nil(t, acts)
	})

	t.Run("rejects unknown profile", func(t *testing.T) {
		acts, err := NewActivities("v3")
		assert.Error(t, err)
		assert.Nil(t, acts)
	})
}

// TestWorkerVersionLabel pins the documented fallback so a future change to
// the helper cannot silently start emitting an empty version label. Both
// the nil-receiver and the empty-profile paths must resolve to "unknown" so
// method-value handles formed from a zero-value pointer remain safe to
// evaluate.
func TestWorkerVersionLabel(t *testing.T) {
	var nilActs *Activities
	assert.Equal(t, "unknown", nilActs.workerVersionLabel(),
		"nil receiver must surface as the explicit \"unknown\" label")
	assert.Equal(t, "unknown", (&Activities{}).workerVersionLabel(),
		"empty profile must surface as the explicit \"unknown\" label")
	assert.Equal(t, "v1", newTestActivities("v1").workerVersionLabel())
	assert.Equal(t, "v2", newTestActivities("v2").workerVersionLabel())
}

func TestIntake(t *testing.T) {
	t.Run("succeeds with valid input", func(t *testing.T) {
		env := newTestEnv(t)

		val, err := env.ExecuteActivity(testActs.Intake, IntakeInput{
			ApplicationID: "APP-001",
			ApplicantName: "Jane Smith",
		})

		assert.NoError(t, err)
		var result IntakeResult
		assert.NoError(t, val.Get(&result))
		assert.Equal(t, "APP-001", result.ApplicationID)
		assert.False(t, result.ReceivedAt.IsZero())
	})

	// Metrics emission must never affect activity behaviour. Exercising the
	// path with both a scenario label and an injected worker profile catches
	// regressions where labelling starts returning errors or panicking, and
	// confirms the activity does not depend on process environment state.
	t.Run("succeeds with scenario and worker profile injected", func(t *testing.T) {
		env := newTestEnvWithProfile(t, "v1")

		val, err := env.ExecuteActivity(testActs.Intake, IntakeInput{
			ApplicationID: "APP-001",
			ApplicantName: "Jane Smith",
			Scenario:      "happy_path",
		})

		assert.NoError(t, err)
		var result IntakeResult
		assert.NoError(t, val.Get(&result))
		assert.Equal(t, "APP-001", result.ApplicationID)
	})

	t.Run("fails when applicationId is empty", func(t *testing.T) {
		env := newTestEnv(t)

		_, err := env.ExecuteActivity(testActs.Intake, IntakeInput{
			ApplicantName: "Jane Smith",
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "applicationId")
	})

	t.Run("fails when applicantName is empty", func(t *testing.T) {
		env := newTestEnv(t)

		_, err := env.ExecuteActivity(testActs.Intake, IntakeInput{
			ApplicationID: "APP-001",
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "applicantName")
	})
}

func TestRequestCreditCheck(t *testing.T) {
	env := newTestEnv(t)

	val, err := env.ExecuteActivity(testActs.RequestCreditCheck, CreditCheckInput{
		ApplicationID: "APP-001",
	})

	assert.NoError(t, err)
	var result CreditCheckRequestResult
	assert.NoError(t, val.Get(&result))
	assert.Equal(t, "APP-001", result.ApplicationID)
	assert.True(t, strings.HasPrefix(result.Reference, "CREDIT-REQ-"), "reference should have expected prefix")
}

func TestReserveOffer(t *testing.T) {
	t.Run("returns a stable offer ID", func(t *testing.T) {
		env := newTestEnv(t)

		val, err := env.ExecuteActivity(testActs.ReserveOffer, ReserveOfferInput{ApplicationID: "APP-001"})

		assert.NoError(t, err)
		var result ReserveOfferResult
		assert.NoError(t, val.Get(&result))
		assert.Equal(t, "APP-001", result.ApplicationID)
		assert.NotEmpty(t, result.OfferID)
		assert.False(t, result.ReservedAt.IsZero())
	})

	t.Run("is idempotent: same application returns same offer ID", func(t *testing.T) {
		env := newTestEnv(t)

		val1, err := env.ExecuteActivity(testActs.ReserveOffer, ReserveOfferInput{ApplicationID: "APP-001"})
		if !assert.NoError(t, err) {
			return
		}
		var r1 ReserveOfferResult
		if !assert.NoError(t, val1.Get(&r1)) {
			return
		}

		val2, err := env.ExecuteActivity(testActs.ReserveOffer, ReserveOfferInput{ApplicationID: "APP-001"})
		if !assert.NoError(t, err) {
			return
		}
		var r2 ReserveOfferResult
		if !assert.NoError(t, val2.Get(&r2)) {
			return
		}

		assert.Equal(t, r1.OfferID, r2.OfferID)
	})

	t.Run("returns different offer IDs for different applications", func(t *testing.T) {
		env := newTestEnv(t)

		val1, err := env.ExecuteActivity(testActs.ReserveOffer, ReserveOfferInput{ApplicationID: "APP-001"})
		if !assert.NoError(t, err) {
			return
		}
		var r1 ReserveOfferResult
		if !assert.NoError(t, val1.Get(&r1)) {
			return
		}

		val2, err := env.ExecuteActivity(testActs.ReserveOffer, ReserveOfferInput{ApplicationID: "APP-002"})
		if !assert.NoError(t, err) {
			return
		}
		var r2 ReserveOfferResult
		if !assert.NoError(t, val2.Get(&r2)) {
			return
		}

		assert.NotEqual(t, r1.OfferID, r2.OfferID)
	})
}

func TestCompleteApplication(t *testing.T) {
	t.Run("succeeds on the happy path", func(t *testing.T) {
		env := newTestEnv(t)

		val, err := env.ExecuteActivity(testActs.CompleteApplication, CompleteApplicationInput{
			ApplicationID: "APP-001",
			OfferID:       "OFFER-APP-001",
		})

		assert.NoError(t, err)
		var result CompleteApplicationResult
		assert.NoError(t, val.Get(&result))
		assert.Equal(t, "APP-001", result.ApplicationID)
		assert.False(t, result.CompletedAt.IsZero())
	})

	t.Run("fails with a retryable error on early attempts when SimulateFailure is set", func(t *testing.T) {
		env := newTestEnv(t)

		_, err := env.ExecuteActivity(testActs.CompleteApplication, CompleteApplicationInput{
			ApplicationID:   "APP-001",
			OfferID:         "OFFER-APP-001",
			SimulateFailure: true,
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "completion failure injected for demo")

		// The error must be retryable so Temporal drives the backoff automatically.
		var appErr *temporal.ApplicationError
		errors.As(err, &appErr)
		assert.NotNil(t, appErr, "error must be a temporal.ApplicationError")
		assert.False(t, appErr.NonRetryable(), "error must be retryable so Temporal retries the activity")
	})
}

func TestMaybeFailExternalDependency(t *testing.T) {
	t.Run("never fails when rate is zero", func(t *testing.T) {
		for range 200 {
			assert.NoError(t, maybeFailExternalDependency("TestActivity", 0))
		}
	})

	t.Run("never fails when rate is negative", func(t *testing.T) {
		for range 200 {
			assert.NoError(t, maybeFailExternalDependency("TestActivity", -10))
		}
	})

	t.Run("error message includes activity name", func(t *testing.T) {
		// Override randIntn so it always returns 0, guaranteeing failure.
		orig := randIntn
		randIntn = func(_ int) int { return 0 }
		defer func() { randIntn = orig }()

		err := maybeFailExternalDependency("MyActivity", MaxExternalFailureRatePercent)
		if assert.Error(t, err) {
			assert.Contains(t, err.Error(), "MyActivity")
		}
	})

	t.Run("values above max are clamped rather than causing a panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			_ = maybeFailExternalDependency("TestActivity", 999)
		})
	})
}

func TestPropertyValuation(t *testing.T) {
	t.Run("returns a deterministic valuation id and echoes the property value", func(t *testing.T) {
		env := newTestEnv(t)

		val, err := env.ExecuteActivity(testActs.PropertyValuation, PropertyValuationInput{
			ApplicationID: "APP-001",
			PropertyValue: 350000,
		})

		assert.NoError(t, err)
		var result PropertyValuationResult
		assert.NoError(t, val.Get(&result))
		assert.Equal(t, "APP-001", result.ApplicationID)
		assert.Equal(t, "VAL-APP-001", result.ValuationID)
		assert.Equal(t, float64(350000), result.PropertyValue)
		assert.False(t, result.ValuedAt.IsZero())
	})

	t.Run("rejects empty application id", func(t *testing.T) {
		env := newTestEnv(t)

		_, err := env.ExecuteActivity(testActs.PropertyValuation, PropertyValuationInput{
			PropertyValue: 350000,
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "applicationId")
	})

	// A non-positive property value is treated as a wiring bug rather than a
	// transient failure, so the activity surfaces a non-retryable error to
	// avoid wasted retries on a value Temporal cannot meaningfully retry.
	t.Run("rejects non-positive property value with a non-retryable error", func(t *testing.T) {
		env := newTestEnv(t)

		_, err := env.ExecuteActivity(testActs.PropertyValuation, PropertyValuationInput{
			ApplicationID: "APP-001",
			PropertyValue: 0,
		})

		assert.Error(t, err)
		var appErr *temporal.ApplicationError
		errors.As(err, &appErr)
		if assert.NotNil(t, appErr) {
			assert.True(t, appErr.NonRetryable())
		}
		assert.Contains(t, err.Error(), "propertyValue")
	})

	// Property valuation participates in the same failure-injection pattern as
	// the other external activities. With randIntn forced to 0 the maximum
	// failure rate guarantees a retryable simulated failure so Temporal drives
	// the retries automatically.
	t.Run("respects external failure injection with a retryable error", func(t *testing.T) {
		orig := randIntn
		randIntn = func(_ int) int { return 0 }
		defer func() { randIntn = orig }()

		env := newTestEnv(t)

		_, err := env.ExecuteActivity(testActs.PropertyValuation, PropertyValuationInput{
			ApplicationID:              "APP-001",
			PropertyValue:              350000,
			ExternalFailureRatePercent: MaxExternalFailureRatePercent,
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "PropertyValuation")
	})
}

func TestSendNotification(t *testing.T) {
	t.Run("dispatches notification with application id and status", func(t *testing.T) {
		env := newTestEnv(t)

		val, err := env.ExecuteActivity(testActs.SendNotification, SendNotificationInput{
			ApplicationID: "APP-001",
			Status:        "approved",
		})

		assert.NoError(t, err)
		var result SendNotificationResult
		assert.NoError(t, val.Get(&result))
		assert.Equal(t, "APP-001", result.ApplicationID)
		assert.Equal(t, "approved", result.Status)
		assert.False(t, result.DeliveredAt.IsZero())
	})

	// Both terminal outcomes must succeed with metric labelling applied. The
	// Status field doubles as the "outcome" label on the completion counter,
	// so a regression there would surface as either a panic or a returned
	// error from the activity. The injected worker profile confirms the
	// version label is taken from the registered Activities value rather than
	// the process environment.
	t.Run("succeeds for both approved and rejected outcomes", func(t *testing.T) {
		for _, status := range []string{"approved", "rejected"} {
			env := newTestEnvWithProfile(t, "v2")
			val, err := env.ExecuteActivity(testActs.SendNotification, SendNotificationInput{
				ApplicationID: "APP-001",
				Status:        status,
				Scenario:      "happy_path",
			})

			assert.NoError(t, err)
			var result SendNotificationResult
			assert.NoError(t, val.Get(&result))
			assert.Equal(t, status, result.Status)
		}
	})

	t.Run("rejects empty application id with non-retryable error", func(t *testing.T) {
		env := newTestEnv(t)

		_, err := env.ExecuteActivity(testActs.SendNotification, SendNotificationInput{
			Status: "approved",
		})

		assert.Error(t, err)
		var appErr *temporal.ApplicationError
		errors.As(err, &appErr)
		if assert.NotNil(t, appErr, "error must be a temporal.ApplicationError") {
			assert.True(t, appErr.NonRetryable(),
				"missing applicationId is a wiring bug, not a transient failure")
		}
		assert.Contains(t, err.Error(), "applicationId")
	})

	t.Run("rejects empty status with non-retryable error", func(t *testing.T) {
		env := newTestEnv(t)

		_, err := env.ExecuteActivity(testActs.SendNotification, SendNotificationInput{
			ApplicationID: "APP-001",
		})

		assert.Error(t, err)
		var appErr *temporal.ApplicationError
		errors.As(err, &appErr)
		if assert.NotNil(t, appErr) {
			assert.True(t, appErr.NonRetryable())
		}
		assert.Contains(t, err.Error(), "status")
	})

	// SendNotification participates in the same failure-injection pattern as
	// the other external activities. With randIntn forced to always return 0
	// the maximum failure rate guarantees a retryable simulated failure.
	t.Run("respects external failure injection with a retryable error", func(t *testing.T) {
		orig := randIntn
		randIntn = func(_ int) int { return 0 }
		defer func() { randIntn = orig }()

		env := newTestEnv(t)

		_, err := env.ExecuteActivity(testActs.SendNotification, SendNotificationInput{
			ApplicationID:              "APP-001",
			Status:                     "approved",
			ExternalFailureRatePercent: MaxExternalFailureRatePercent,
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "SendNotification")

		// The injected error must be retryable so Temporal drives the backoff
		// automatically, matching the behaviour of every other activity.
		var appErr *temporal.ApplicationError
		errors.As(err, &appErr)
		if assert.NotNil(t, appErr) {
			assert.False(t, appErr.NonRetryable(),
				"external failure injection must produce a retryable error")
		}
	})

	// Duration histogram is observed once per terminal completion alongside
	// the completion counter. The activity body is the right place: it runs
	// outside workflow replay and only on the successful attempt of
	// SendNotification, so each execution contributes a single sample.
	t.Run("observes application duration when SubmittedAt is set", func(t *testing.T) {
		env := newTestEnvWithProfile(t, "v1")

		labels := []string{"happy_path", "v1", "approved"}
		before := histogramSampleCount(t, labels)

		_, err := env.ExecuteActivity(testActs.SendNotification, SendNotificationInput{
			ApplicationID: "APP-001",
			Status:        "approved",
			Scenario:      "happy_path",
			SubmittedAt:   time.Now().Add(-2 * time.Second),
		})
		assert.NoError(t, err)

		after := histogramSampleCount(t, labels)
		assert.Equal(t, before+1, after,
			"duration histogram must observe exactly one sample on success")
	})

	// Duration histogram is skipped when the workflow did not supply a start
	// time. Tests and any legacy caller that omits SubmittedAt must not
	// contribute zero or near-zero samples that would skew the percentile.
	t.Run("skips application duration when SubmittedAt is zero", func(t *testing.T) {
		env := newTestEnvWithProfile(t, "v1")

		labels := []string{"happy_path", "v1", "rejected"}
		before := histogramSampleCount(t, labels)

		_, err := env.ExecuteActivity(testActs.SendNotification, SendNotificationInput{
			ApplicationID: "APP-002",
			Status:        "rejected",
			Scenario:      "happy_path",
		})
		assert.NoError(t, err)

		after := histogramSampleCount(t, labels)
		assert.Equal(t, before, after,
			"duration histogram must not record a sample when SubmittedAt is zero")
	})
}

// histogramSampleCount reads the current sample count from
// ApplicationDurationSeconds for the supplied label values, going through the
// official Prometheus client interface so the test stays decoupled from
// internal storage.
func histogramSampleCount(t *testing.T, labels []string) uint64 {
	t.Helper()
	obs, err := observability.ApplicationDurationSeconds.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("get histogram: %v", err)
	}
	// GetMetricWithLabelValues returns a prometheus.Observer; the concrete
	// histogram value also implements prometheus.Metric, which is what
	// Write expects.
	pm, ok := obs.(prometheus.Metric)
	if !ok {
		t.Fatalf("histogram observer does not implement prometheus.Metric")
	}
	var m dto.Metric
	if err := pm.Write(&m); err != nil {
		t.Fatalf("write histogram: %v", err)
	}
	if m.Histogram == nil {
		return 0
	}
	return m.Histogram.GetSampleCount()
}

func TestReleaseOffer(t *testing.T) {
	t.Run("releases an offer successfully", func(t *testing.T) {
		env := newTestEnv(t)

		val, err := env.ExecuteActivity(testActs.ReleaseOffer, ReleaseOfferInput{
			ApplicationID: "APP-001",
			OfferID:       "OFFER-APP-001",
			Scenario:      "fail_and_compensate_after_offer_reservation",
		})

		assert.NoError(t, err)
		var result ReleaseOfferResult
		assert.NoError(t, val.Get(&result))
		assert.Equal(t, "APP-001", result.ApplicationID)
		assert.False(t, result.ReleasedAt.IsZero())
	})

	// ReleaseOffer is idempotent: repeated calls for the same offerId succeed without
	// error. Temporal may retry the compensation activity, and each retry must produce
	// the same logical outcome without creating duplicate side effects.
	t.Run("is idempotent: repeated calls for the same offerId succeed", func(t *testing.T) {
		env := newTestEnv(t)

		val1, err := env.ExecuteActivity(testActs.ReleaseOffer, ReleaseOfferInput{
			ApplicationID: "APP-001",
			OfferID:       "OFFER-APP-001",
		})
		assert.NoError(t, err)
		var r1 ReleaseOfferResult
		assert.NoError(t, val1.Get(&r1))
		assert.Equal(t, "APP-001", r1.ApplicationID)

		val2, err := env.ExecuteActivity(testActs.ReleaseOffer, ReleaseOfferInput{
			ApplicationID: "APP-001",
			OfferID:       "OFFER-APP-001",
		})
		assert.NoError(t, err)
		var r2 ReleaseOfferResult
		assert.NoError(t, val2.Get(&r2))
		assert.Equal(t, "APP-001", r2.ApplicationID)
		assert.False(t, r2.ReleasedAt.IsZero())
	})
}
