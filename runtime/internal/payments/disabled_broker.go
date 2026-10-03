package payments

type disabledBroker struct{}

func (disabledBroker) Authorize(AuthorizeRequest) (SignedAuthorization, error) {
	return SignedAuthorization{}, ErrUnconfigured
}
