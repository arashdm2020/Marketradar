package bnb

import "context"

type SignRequest struct {
	PaymentHash        string
	Source, Destination string
	DestinationVersion int
	Nonce, ChainID     uint64
	AmountWei, GasPriceWei string
	GasLimit           uint64
}
type Signed struct{ Raw, Hash string }
type Signer interface {
	Sign(context.Context, SignRequest) (Signed, error)
}
