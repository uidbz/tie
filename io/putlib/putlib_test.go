package putlib

import (
	"testing"
)

const license_hash = "f237c0e59ea0166af622b855b7c933cb37e25ed233048f7d85e22ef714111a02"

func TestAddressOfFile(t *testing.T) {
	pc := &PutConfig{}
	if hash, err := pc.AddressOfFile("../../LICENSE"); err != nil {
		t.Error(err)
	} else {
		if hash != license_hash {
			t.Error("Wrong license")
		}
	}
}
