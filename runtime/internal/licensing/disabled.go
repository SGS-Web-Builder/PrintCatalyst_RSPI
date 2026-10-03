package licensing

type disabledControlPlane struct{}

func (disabledControlPlane) IssueLicense(IssueRequest) (SignedLicense, error) {
	return SignedLicense{}, ErrUnconfigured
}
