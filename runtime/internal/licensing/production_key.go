//go:build production

package licensing

// No licence/payment fixture signing secret may be shipped to merchants.
const ControlPlanePrivateKeyHex = ""
