package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func instanceForTest(name, cname, region string) ICLInstanceConfig {
	return ICLInstanceConfig{
		Name: name,
		CRN:  MustCRNFromString("crn:v1:" + cname + ":public:logs:" + region + ":a/account:instance::"),
	}
}

func TestEffectiveInstances(t *testing.T) {
	t.Parallel()

	cfg := &Config{ICL: ICL{Instances: []ICLInstanceConfig{
		instanceForTest("one", "bluemix", "us-south"),
		instanceForTest("two", "staging", "eu-de"),
	}}}
	instances := EffectiveInstances(cfg)
	require.Len(t, instances, 2)
	assert.Equal(t, []string{"one", "two"}, []string{instances[0].Name, instances[1].Name})
	assert.NotNil(t, instances)
}

func TestEffectiveInstances_DoesNotAliasConfig(t *testing.T) {
	t.Parallel()

	cfg := &Config{ICL: ICL{Instances: []ICLInstanceConfig{instanceForTest("one", "bluemix", "us-south")}}}
	instances := EffectiveInstances(cfg)
	require.Len(t, instances, 1)
	assert.NotSame(t, &cfg.ICL.Instances[0], &instances[0])

	instances[0].Name = "mutated"
	assert.Equal(t, "one", cfg.ICL.Instances[0].Name)
}

func TestNewConfig_InstancesAreEmptyAndNonNil(t *testing.T) {
	t.Parallel()

	instances := EffectiveInstances(New())
	assert.NotNil(t, instances)
	assert.Empty(t, instances)
}

func TestConfigValidate_RejectsMissingInstancesListAndFields(t *testing.T) {
	t.Parallel()
	validCRN := MustCRNFromString("crn:v1:bluemix:public:logs:us-south:a/account:instance::")
	environments := map[string]ICLEnvironmentConfig{
		"bluemix":    {IAMURL: "https://iam.example/identity"},
		"test-cloud": {IAMURL: "https://iam.example/test"},
	}
	tests := []struct {
		name string
		icl  ICL
		want string
	}{
		{"nil list", ICL{}, "icl.instances must be a list"},
		{"missing name", ICL{Instances: []ICLInstanceConfig{{CRN: validCRN}}, Environments: environments}, "effective instance is missing a name"},
		{"missing CRN", ICL{Instances: []ICLInstanceConfig{{Name: "missing"}}, Environments: environments}, `effective instance "missing" is missing a CRN`},
		{"duplicate name", ICL{Instances: []ICLInstanceConfig{instanceForTest("same", "bluemix", "us-south"), instanceForTest("same", "test-cloud", "eu-de")}, Environments: environments}, `duplicate effective instance name "same"`},
		{"duplicate CRN", ICL{Instances: []ICLInstanceConfig{instanceForTest("one", "bluemix", "us-south"), instanceForTest("two", "bluemix", "us-south")}, Environments: environments}, "duplicate effective instance CRN"},
		{"slash in name", ICL{Instances: []ICLInstanceConfig{instanceForTest("region/label", "bluemix", "us-south")}, Environments: environments}, "must not contain /"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.ErrorContains(t, (&Config{ICL: tt.icl}).Validate(), tt.want)
		})
	}
}

func TestDisplayNameForCRN(t *testing.T) {
	t.Parallel()
	configured := instanceForTest("configured", "bluemix", "us-south")
	shortID := MustCRNFromString("crn:v1:test-cloud:public:logs:eu-de:a/account:short::")
	longID := MustCRNFromString("crn:v1:bluemix:public:logs:ca-tor:a/account:123456789::")

	assert.Equal(t, "configured", DisplayNameForCRN([]ICLInstanceConfig{configured}, configured.CRN))
	assert.Equal(t, "eu-de/short", DisplayNameForCRN(nil, shortID))
	assert.Equal(t, "ca-tor/12345678", DisplayNameForCRN(nil, longID))
}
