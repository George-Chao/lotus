package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/chain/stmgr"
	types "github.com/filecoin-project/lotus/chain/types"
	cid "github.com/ipfs/go-cid"
	cbg "github.com/whyrusleeping/cbor-gen"
	"golang.org/x/xerrors"
)

type ServicesAPI interface {
	// Sends executes a send given SendParams
	Send(ctx context.Context, params SendParams) (cid.Cid, error)
	// DecodeTypedParamsFromJSON takes in information needed to identify a method and converts JSON
	// paramaters to bytes of their CBOR encoding
	DecodeTypedParamsFromJSON(ctx context.Context, to address.Address, method abi.MethodNum, paramstr string) ([]byte, error)

	// Close ends the session of services and disconnects from RPC, using Services after Close is called
	// most likely will result in an error
	// Should not be called concurrently
	Close() error
}

type ServicesImpl struct {
	api    api.FullNode
	closer jsonrpc.ClientCloser
}

func (s *ServicesImpl) Close() error {
	if s.closer == nil {
		return xerrors.Errorf("Services already closed")
	}
	s.closer()
	s.closer = nil
	return nil
}

func (s *ServicesImpl) DecodeTypedParamsFromJSON(ctx context.Context, to address.Address, method abi.MethodNum, paramstr string) ([]byte, error) {
	act, err := s.api.StateGetActor(ctx, to, types.EmptyTSK)
	if err != nil {
		return nil, err
	}

	methodMeta, found := stmgr.MethodsMap[act.Code][method]
	if !found {
		return nil, fmt.Errorf("method %d not found on actor %s", method, act.Code)
	}

	p := reflect.New(methodMeta.Params.Elem()).Interface().(cbg.CBORMarshaler)

	if err := json.Unmarshal([]byte(paramstr), p); err != nil {
		return nil, fmt.Errorf("unmarshaling input into params type: %w", err)
	}

	buf := new(bytes.Buffer)
	if err := p.MarshalCBOR(buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type SendParams struct {
	To   address.Address
	From address.Address
	Val  abi.TokenAmount

	GasPremium abi.TokenAmount
	GasFeeCap  abi.TokenAmount
	GasLimit   int64

	Nonce struct {
		N   uint64
		Set bool
	}
	Method abi.MethodNum
	Params []byte

	Force bool
}

// This is specialised Send for Send command
// There might be room for generic Send that other commands can use to send their messages
// We will see

func (s *ServicesImpl) Send(ctx context.Context, params SendParams) (cid.Cid, error) {
	if params.From == address.Undef {
		defaddr, err := s.api.WalletDefaultAddress(ctx)
		if err != nil {
			return cid.Undef, err
		}
		params.From = defaddr
	}

	msg := &types.Message{
		From:  params.From,
		To:    params.To,
		Value: params.Val,

		GasPremium: params.GasPremium,
		GasFeeCap:  params.GasFeeCap,
		GasLimit:   params.GasLimit,

		Method: params.Method,
		Params: params.Params,
	}

	if !params.Force {
		// Funds insufficient check
		fromBalance, err := s.api.WalletBalance(ctx, msg.From)
		if err != nil {
			return cid.Undef, err
		}
		totalCost := types.BigAdd(types.BigMul(msg.GasFeeCap, types.NewInt(uint64(msg.GasLimit))), msg.Value)

		if fromBalance.LessThan(totalCost) {
			fmt.Printf("WARNING: From balance %s less than total cost %s\n", types.FIL(fromBalance), types.FIL(totalCost))
			return cid.Undef, fmt.Errorf("--force must be specified for this action to have an effect; you have been warned")
		}
	}

	if params.Nonce.Set {
		msg.Nonce = params.Nonce.N
		sm, err := s.api.WalletSignMessage(ctx, params.From, msg)
		if err != nil {
			return cid.Undef, err
		}

		_, err = s.api.MpoolPush(ctx, sm)
		if err != nil {
			return cid.Undef, err
		}

		return sm.Cid(), nil
	}

	sm, err := s.api.MpoolPushMessage(ctx, msg, nil)
	if err != nil {
		return cid.Undef, err
	}

	return sm.Cid(), nil
}
