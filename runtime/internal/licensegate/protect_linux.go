//go:build linux

package licensegate

import "github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/devicekeys"

const licenceKeyPurpose = "licence-device-private-key"

func protect(raw []byte) ([]byte, error) {
	v, err := devicekeys.Load()
	if err != nil {
		return nil, err
	}
	return v.Seal(licenceKeyPurpose, raw)
}
func unprotect(raw []byte) ([]byte, error) {
	v, err := devicekeys.Load()
	if err != nil {
		return nil, err
	}
	return v.Open(licenceKeyPurpose, raw)
}
