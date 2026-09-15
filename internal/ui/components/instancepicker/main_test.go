package instancepicker

import (
	"testing"

	"go.uber.org/goleak"
)

func testCRN(name string) string {
	return "crn:v1:bluemix:public:logs:us-south:a/" + name + ":" + name + "::"
}

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
