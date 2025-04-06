package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDestinationHealthInterval(t *testing.T) {
	d := Destination{Name: "x", URL: "http://meili:7700"}
	require.NoError(t, d.Validate())
	assert.Equal(t, DefaultHealthInterval, d.HealthCheckInterval.D(), "unset gets the default")

	for _, ok := range []time.Duration{5 * time.Second, time.Minute, time.Hour} {
		d.HealthCheckInterval = Duration(ok)
		assert.NoError(t, d.Validate(), ok)
		assert.Equal(t, ok, d.HealthEvery())
	}
	for _, bad := range []time.Duration{time.Second, 4 * time.Second, 2 * time.Hour, -time.Second} {
		d.HealthCheckInterval = Duration(bad)
		assert.ErrorContains(t, d.Validate(), "healthCheckInterval", bad)
	}

	// Stored before the setting existed, or out of range: clamped.
	assert.Equal(t, DefaultHealthInterval, Destination{}.HealthEvery())
	assert.Equal(t, MinHealthInterval, Destination{HealthCheckInterval: Duration(time.Second)}.HealthEvery())
	assert.Equal(t, MaxHealthInterval, Destination{HealthCheckInterval: Duration(5 * time.Hour)}.HealthEvery())
}
