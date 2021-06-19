package putlib

import (
	"testing"
)

const license_hash = "29772659acbae6471b57f25df3aba740a639f7bd30f523f5a60ef6040531fc50"

func TestAddressOfFile(t *testing.T) {
	if hash, err := AddressOfFile("LICENSE"); err != nil {
		t.Error(err)
	} else {
		if hash != license_hash {
			t.Error("Wrong license")
		}
	}
}
