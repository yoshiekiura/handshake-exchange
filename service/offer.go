package service

import (
	"fmt"
	"github.com/go-errors/errors"
	"github.com/ninjadotorg/handshake-exchange/api_error"
	"github.com/ninjadotorg/handshake-exchange/bean"
	"github.com/ninjadotorg/handshake-exchange/common"
	"github.com/ninjadotorg/handshake-exchange/dao"
	"github.com/ninjadotorg/handshake-exchange/integration/blockchainio_service"
	"github.com/ninjadotorg/handshake-exchange/integration/coinbase_service"
	"github.com/ninjadotorg/handshake-exchange/integration/crypto_service"
	"github.com/ninjadotorg/handshake-exchange/integration/solr_service"
	"github.com/ninjadotorg/handshake-exchange/service/notification"
	"github.com/shopspring/decimal"
	"time"
)

type OfferService struct {
	dao      *dao.OfferDao
	userDao  *dao.UserDao
	transDao *dao.TransactionDao
	miscDao  *dao.MiscDao
}

func (s OfferService) GetOffer(userId string, offerId string) (offer bean.Offer, ce SimpleContextError) {
	if GetProfile(s.userDao, userId, &ce); ce.HasError() {
		return
	}
	if offer = *GetOffer(*s.dao, offerId, &ce); ce.HasError() {
		return
	}

	price := common.StringToDecimal(offer.Price)
	percentage := common.StringToDecimal(offer.Percentage)

	price, fiatPrice, fiatAmount, err := s.GetQuote(offer.Type, offer.Amount, offer.Currency, offer.FiatCurrency)
	if offer.IsTypeSell() && price.Equal(common.Zero) {
		if ce.SetError(api_error.GetDataFailed, err) {
			return
		}
		markup := fiatAmount.Mul(percentage)
		fiatAmount = fiatAmount.Add(markup)
	}
	offer.Price = common.DecimalToFiatString(fiatPrice)
	offer.FiatAmount = common.DecimalToFiatString(fiatAmount)

	return
}

func (s OfferService) CreateOffer(userId string, offerBody bean.Offer) (offer bean.Offer, ce SimpleContextError) {
	currencyInst := bean.CurrencyMapping[offerBody.Currency]
	if currencyInst.Code == "" {
		ce.SetStatusKey(api_error.UnsupportedCurrency)
		return
	}

	// Minimum amount
	amount, errFmt := decimal.NewFromString(offerBody.Amount)
	if ce.SetError(api_error.InvalidRequestBody, errFmt) {
		return
	}
	if currencyInst.Code == bean.ETH.Code {
		if amount.LessThan(bean.MIN_ETH) {
			ce.SetStatusKey(api_error.AmountIsTooSmall)
			return
		}
	}
	if currencyInst.Code == bean.BTC.Code {
		if amount.LessThan(bean.MIN_BTC) {
			ce.SetStatusKey(api_error.AmountIsTooSmall)
			return
		}
	}

	// Set Status
	if offerBody.IsTypeBuy() {
		// Need to set address to receive crypto
		if offerBody.UserAddress == "" && offerBody.Type == bean.BTC.Code {
			ce.SetStatusKey(api_error.InvalidRequestBody)
			return
		}
		offerBody.Status = bean.OFFER_STATUS_ACTIVE
	} else {
		if offerBody.RefundAddress == "" && offerBody.Type == bean.BTC.Code {
			ce.SetStatusKey(api_error.InvalidRequestBody)
			return
		}
		offerBody.Status = bean.OFFER_STATUS_CREATED
	}

	// Set percentage
	if offerBody.Percentage != "" {
		// Convert to 0.0x
		percentage, errFmt := decimal.NewFromString(offerBody.Percentage)
		if ce.SetError(api_error.InvalidRequestBody, errFmt) {
			return
		}
		offerBody.Percentage = percentage.Div(decimal.NewFromFloat(100)).String()
	} else {
		offerBody.Percentage = "0"
	}

	// Check user valid
	profile := *GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	if UserServiceInst.CheckOfferLocked(profile) {
		ce.SetStatusKey(api_error.OfferActionLocked)
		return
	}
	offerBody.UID = userId

	transCountTO := s.transDao.GetTransactionCount(offerBody.UID, offerBody.Currency)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, transCountTO) {
		return
	}
	offerBody.TransactionCount = transCountTO.Object.(bean.TransactionCount)

	s.generateSystemAddress(&offerBody, &ce)
	if ce.HasError() {
		return
	}
	s.setupOfferAmount(&offerBody, &ce)
	if ce.HasError() {
		return
	}

	var err error
	offer, err = s.dao.AddOffer(offerBody, profile)
	if ce.SetError(api_error.AddDataFailed, err) {
		return
	}

	offer.CreatedAt = time.Now().UTC()
	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) ActiveOffer(address string, amountStr string) (offer bean.Offer, ce SimpleContextError) {
	addressMapTO := s.dao.GetOfferAddress(address)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, addressMapTO) {
		return
	}
	if ce.NotFound {
		ce.SetStatusKey(api_error.ResourceNotFound)
		return
	}
	addressMap := addressMapTO.Object.(bean.OfferAddressMap)

	if offer = *GetOffer(*s.dao, addressMap.Offer, &ce); ce.HasError() {
		return
	}
	if offer.Status != bean.OFFER_STATUS_CREATED {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	inputAmount := common.StringToDecimal(amountStr)
	offerAmount := common.StringToDecimal(offer.Amount)
	totalAmount := common.StringToDecimal(offer.TotalAmount)

	// Check amount need to deposit
	sub := decimal.NewFromFloat(1)
	if offer.IsTypeBuy() {
		sub = offerAmount.Sub(inputAmount)
	} else {
		sub = totalAmount.Sub(inputAmount)
	}

	if sub.Equal(common.Zero) {
		// Good
		offer.Status = bean.OFFER_STATUS_ACTIVE
		err := s.dao.UpdateOfferActive(offer)
		if ce.SetError(api_error.UpdateDataFailed, err) {
			return
		}

		notification.SendOfferNotification(offer)
	} else {
		ce.SetStatusKey(api_error.InvalidAmount)
		return
	}

	return
}

func (s OfferService) ActiveOnChainOffer(offerId string, hid int64) (offer bean.Offer, ce SimpleContextError) {
	if offer = *GetOffer(*s.dao, offerId, &ce); ce.HasError() {
		return
	}
	if offer.Status != bean.OFFER_STATUS_CREATED && offer.Status != bean.OFFER_STATUS_PRE_SHAKING {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	// Good
	offer.Hid = hid
	if offer.Status == bean.OFFER_STATUS_CREATED {
		offer.Status = bean.OFFER_STATUS_ACTIVE
		err := s.dao.UpdateOfferActive(offer)
		if ce.SetError(api_error.UpdateDataFailed, err) {
			return
		}
	} else if offer.Status == bean.OFFER_STATUS_PRE_SHAKING {
		offer.Status = bean.OFFER_STATUS_PRE_SHAKE
		_, ce = s.PreShakeOnChainOffer(offerId, hid)
		if ce.HasError() {
			return
		}
	}

	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) CloseOffer(userId string, offerId string) (offer bean.Offer, ce SimpleContextError) {
	if GetProfile(s.userDao, userId, &ce); ce.HasError() {
		return
	}
	if offer = *GetOffer(*s.dao, offerId, &ce); ce.HasError() {
		return
	}
	if offer.Status != bean.OFFER_STATUS_ACTIVE {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	if offer.Status == bean.BTC.Code {
		offer.Status = bean.OFFER_STATUS_CLOSED
	} else {
		// Only ETH
		if offer.IsTypeSell() {
			// Waiting for smart contract
			offer.Status = bean.OFFER_STATUS_CLOSING
		} else {
			offer.Status = bean.OFFER_STATUS_CLOSED
		}
	}

	err := s.dao.UpdateOfferClose(offer, bean.Profile{})
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) CloseFailedOffer(userId, offerId string) (offer bean.Offer, ce SimpleContextError) {
	if offer = *GetOffer(*s.dao, offerId, &ce); ce.HasError() {
		return
	}
	if offer.Status != bean.OFFER_STATUS_CREATED {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	offer.Status = bean.OFFER_STATUS_CREATE_FAILED
	err := s.dao.UpdateOfferClose(offer, bean.Profile{})
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}
	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) ShakeOffer(userId string, offerId string, body bean.OfferShakeRequest) (offer bean.Offer, ce SimpleContextError) {
	profile := *GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	if UserServiceInst.CheckOfferLocked(profile) {
		ce.SetStatusKey(api_error.OfferActionLocked)
		return
	}

	if offer = *GetOffer(*s.dao, offerId, &ce); ce.HasError() {
		return
	}
	if profile.UserId == offer.UID {
		ce.SetStatusKey(api_error.OfferPayMyself)
		return
	}
	offer.ToUID = userId

	if offer.Status != bean.OFFER_STATUS_ACTIVE {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	s.setupOfferPrice(&offer, &ce)
	if ce.HasError() {
		return
	}
	if offer.IsTypeSell() {
		if body.Address == "" && offer.Currency == bean.BTC.Code {
			// Only BTC needs to check
			ce.SetStatusKey(api_error.InvalidRequestBody)
			return
		}
		offer.UserAddress = body.Address
		offer.Status = bean.OFFER_STATUS_SHAKE
	} else {
		if offer.Currency == bean.BTC.Code {
			if body.Address == "" {
				// Only BTC needs to check
				ce.SetStatusKey(api_error.InvalidRequestBody)
				return
			}
			offer.RefundAddress = body.Address
			offer.Status = bean.OFFER_STATUS_SHAKING
		} else {
			offer.Status = bean.OFFER_STATUS_PRE_SHAKING
		}

	}

	offer.ToEmail = body.Email
	offer.ToUsername = body.Username
	offer.ToChatUsername = body.ChatUsername
	offer.ToLanguage = body.Language
	offer.ToFCM = body.FCM

	err := s.dao.UpdateOfferShaking(offer)
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) UpdateShakeOffer(offerBody bean.Offer) (offer bean.Offer, ce SimpleContextError) {
	if offerBody.Status != bean.OFFER_STATUS_SHAKING {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	// Good
	offerBody.Status = bean.OFFER_STATUS_SHAKE

	err := s.dao.UpdateOfferShake(offerBody)
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	offer = offerBody
	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) AcceptShakeOffer(userId string, offerId string) (offer bean.Offer, ce SimpleContextError) {
	profile := GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOffer(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}

	if profile.UserId != offer.UID {
		ce.SetStatusKey(api_error.InvalidRequestBody)
		return
	}

	if offer.Status != bean.OFFER_STATUS_PRE_SHAKE {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
	}

	if offer.Currency == bean.BTC.Code {
		offer.Status = bean.OFFER_STATUS_SHAKE
	} else {
		// Only ETH
		offer.Status = bean.OFFER_STATUS_SHAKING
	}

	err := s.dao.UpdateOffer(offer, offer.GetChangeStatus())
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	offer.ActionUID = userId
	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) CancelShakeOffer(userId string, offerId string) (offer bean.Offer, ce SimpleContextError) {
	profile := GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOffer(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}

	if profile.UserId != offer.UID && profile.UserId != offer.ToUID {
		ce.SetStatusKey(api_error.InvalidRequestBody)
		return
	}

	if offer.Status != bean.OFFER_STATUS_PRE_SHAKE {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
	}

	if offer.Currency == bean.BTC.Code {
		offer.ToUID = ""
		offer.Status = bean.OFFER_STATUS_CANCELLED
		// Need it here to duplicate solr record for cancelled
		notification.SendOfferNotification(offer)
		// Only BTC refund
		s.transferCrypto(&offer, userId, &ce)
		if ce.HasError() {
			return
		}
		offer.Status = bean.OFFER_STATUS_ACTIVE
	} else {
		// Only ETH
		offer.Status = bean.OFFER_STATUS_CANCELLING
	}

	err := s.dao.UpdateOffer(offer, offer.GetChangeStatus())
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	offer.ActionUID = userId
	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) CancelFailedShakeOffer(userId string, offerId string) (offer bean.Offer, ce SimpleContextError) {
	offer = *GetOffer(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	if offer.Status != bean.OFFER_STATUS_PRE_SHAKING {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
	}

	offer.ToUID = ""
	offer.Status = bean.OFFER_STATUS_PRE_SHAKE_FAILED
	// Need it here to duplicate solr record for cancelled
	notification.SendOfferNotification(offer)
	offer.Status = bean.OFFER_STATUS_ACTIVE

	err := s.dao.UpdateOffer(offer, offer.GetChangeStatus())
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}
	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) RejectShakeOffer(userId string, offerId string) (offer bean.Offer, ce SimpleContextError) {
	profile := *GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOffer(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}

	offerProfile := s.getOfferProfile(offer, profile, &ce)
	offerProfile.ActiveOffers[offer.Currency] = false

	if ce.HasError() {
		return
	}

	if profile.UserId != offer.UID && profile.UserId != offer.ToUID {
		ce.SetStatusKey(api_error.InvalidRequestBody)
		return
	}

	if offer.Status != bean.OFFER_STATUS_SHAKE {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
	}

	if offer.Currency == bean.BTC.Code {
		offer.Status = bean.OFFER_STATUS_REJECTED
		UserServiceInst.UpdateOfferRejectLock(profile)
	} else {
		// Only ETH
		offer.Status = bean.OFFER_STATUS_REJECTING
	}
	transCount := s.getFailedTransCount(offer)
	err := s.dao.UpdateOfferReject(offer, offerProfile, transCount)
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	offer.ActionUID = userId
	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) CompleteShakeOffer(userId string, offerId string) (offer bean.Offer, ce SimpleContextError) {
	profile := *GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOffer(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}

	if offer.IsTypeSell() {
		if offer.UID != userId {
			ce.SetStatusKey(api_error.InvalidUserToCompleteHandshake)
		}
	} else {
		if offer.ToUID != userId {
			ce.SetStatusKey(api_error.InvalidUserToCompleteHandshake)
		}
	}

	if ce.HasError() {
		return
	}

	if profile.UserId != offer.UID && offer.IsTypeSell() {
		ce.SetStatusKey(api_error.InvalidRequestBody)
		return
	}
	if profile.UserId != offer.ToUID && offer.Type == bean.OFFER_TYPE_BUY {
		ce.SetStatusKey(api_error.InvalidRequestBody)
		return
	}

	if offer.Status != bean.OFFER_STATUS_SHAKE {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	if offer.Currency == bean.BTC.Code {
		// Do Transfer
		s.transferCrypto(&offer, userId, &ce)
		if ce.HasError() {
			return
		}
		offer.Status = bean.OFFER_STATUS_COMPLETED

		offerProfile := s.getOfferProfile(offer, profile, &ce)
		offerProfile.ActiveOffers[offer.Currency] = false

		transCount := s.getSuccessTransCount(offer)
		err := s.dao.UpdateOfferCompleted(offer, offerProfile, transCount)
		if ce.SetError(api_error.UpdateDataFailed, err) {
			return
		}
	} else {
		offer.Status = bean.OFFER_STATUS_COMPLETING
		err := s.dao.UpdateOffer(offer, offer.GetChangeStatus())
		if ce.SetError(api_error.UpdateDataFailed, err) {
			return
		}
	}

	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) UpdateOfferToPreviousStatus(userId string, offerId string) (offer bean.Offer, ce SimpleContextError) {
	offer = *GetOffer(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	if offer.Status == bean.OFFER_STATUS_SHAKING {
		if offer.Currency == bean.ETH.Code {
			offer.Status = bean.OFFER_STATUS_PRE_SHAKE
		} else {
			offer.Status = bean.OFFER_STATUS_ACTIVE
		}
	} else if offer.Status == bean.OFFER_STATUS_CANCELLING {
		offer.Status = bean.OFFER_STATUS_PRE_SHAKE
	} else if offer.Status == bean.OFFER_STATUS_REJECTING {
		offer.Status = bean.OFFER_STATUS_SHAKE
	} else if offer.Status == bean.OFFER_STATUS_COMPLETING {
		offer.Status = bean.OFFER_STATUS_SHAKE
	} else {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
	}

	err := s.dao.UpdateOffer(offer, offer.GetChangeStatus())
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}
	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) OnChainOfferTracking(userId string, offerId string, body bean.OfferOnChainTransaction) (offer bean.Offer, ce SimpleContextError) {
	profile := *GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOffer(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}

	onChainTracking := bean.OfferOnChainActionTracking{
		UID:      profile.UserId,
		Offer:    offer.Id,
		OfferRef: dao.GetOfferItemPath(offer.Id),
		Type:     bean.OFFER_ADDRESS_MAP_OFFER,
		TxHash:   body.TxHash,
		Currency: offer.Currency,
		Action:   body.Action,
		Reason:   body.Reason,
	}

	err := s.dao.AddOfferOnChainActionTracking(onChainTracking)
	if ce.SetError(api_error.AddDataFailed, err) {
		return
	}

	return
}

func (s OfferService) CheckOfferOnChainTransaction() error {
	originalList, err1 := s.dao.ListOfferOnChainActionTracking(true)
	onChainList, err2 := s.dao.ListOfferOnChainActionTracking(false)
	if err1 != nil {
		return err1
	}
	if err2 != nil {
		return err2
	}

	onChainMap := map[string]bean.OfferOnChainActionTracking{}
	for _, item := range onChainList {
		onChainMap[item.OfferRef] = item
	}
	for _, item := range originalList {
		txOk := true
		onChainItem, ok := onChainMap[item.OfferRef]
		if ok {
			txHash := onChainItem.TxHash
			if txHash != "" {
				fmt.Println("There is on chain tx hash")
				fmt.Println(txHash)
				isSuccess, isPending, err := crypto_service.GetTransactionReceipt(txHash, item.Currency)
				fmt.Printf("%s %s %s", isSuccess, isPending, err)
				if err == nil {
					// Completed and failed
					if !isPending {
						if !isSuccess {
							txOk = false
						} else {
							s.dao.RemoveOfferOnChainActionTracking(item.Id, true)
						}
					}
				}
			} else {
				fmt.Println("There is NO on chain tx hash")
				txOk = false
			}
		} else {
			fmt.Println("There is NO on chain tx hash")
			fmt.Println(int64(time.Now().UTC().Sub(item.CreatedAt).Minutes()))
			// Reverse the status if there is no tx hash within 5 minutes
			if int64(time.Now().UTC().Sub(item.CreatedAt).Minutes()) > 5 {
				txOk = false
			}
		}

		if !txOk {
			if item.Type == bean.OFFER_ADDRESS_MAP_OFFER {
				_, ce := s.RevertOfferAction(item.UID, item.OfferRef)
				fmt.Println(ce.Error)
			} else if item.Type == bean.OFFER_ADDRESS_MAP_OFFER_STORE {
				if item.Action == bean.OFFER_STORE_STATUS_CREATED {
					_, ce := OfferStoreServiceInst.RemoveFailedOfferStoreItem(item.UID, item.Offer, item.Currency)
					fmt.Println(ce.Error)
				} else if item.Action == bean.OFFER_STORE_STATUS_CLOSING {
					_, ce := OfferStoreServiceInst.OpenCloseFailedOfferStore(item.UID, item.Offer, item.Currency)
					fmt.Println(ce.Error)
				}
			} else if item.Type == bean.OFFER_ADDRESS_MAP_OFFER_STORE_ITEM {
				_, ce := OfferStoreServiceInst.CancelRefillOfferStoreItem(item.UID, item.Offer, item.Currency)
				fmt.Println(ce.Error)
			} else if item.Type == bean.OFFER_ADDRESS_MAP_OFFER_STORE_SHAKE {
				_, ce := OfferStoreServiceInst.UpdateOfferShakeToPreviousStatus(item.UID, item.Offer)
				fmt.Println(ce.Error)
			}
			s.dao.RemoveOfferOnChainActionTracking(item.Id, false)
		}
	}

	return nil
}

func (s OfferService) UpdateOnChainOffer(offerId string, hid int64, oldStatus string, newStatus string) (offer bean.Offer, ce SimpleContextError) {
	offer = *GetOffer(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	profile := *GetProfile(s.userDao, offer.UID, &ce)
	if ce.HasError() {
		return
	}

	// Special case
	if oldStatus == bean.OFFER_STATUS_REJECTING {
		if offer.Status == bean.OFFER_STATUS_CLOSING {
			oldStatus = bean.OFFER_STATUS_CLOSING
			newStatus = bean.OFFER_STATUS_CLOSED
		} else if offer.Status == bean.OFFER_STATUS_CANCELLING {
			oldStatus = bean.OFFER_STATUS_CANCELLING
			newStatus = bean.OFFER_STATUS_ACTIVE

			offer.Status = bean.OFFER_STATUS_CANCELLED
			// Need it here to duplicate solr record for cancelled for both maker and taker
			notification.SendOfferNotification(offer)
			offer.Status = bean.OFFER_STATUS_CANCELLING
			// So the last notification only update for maker
			offer.ToUID = ""
		} else if offer.Status == bean.OFFER_STATUS_REJECTING {
			UserServiceInst.UpdateOfferRejectLock(profile)
		}
	}

	if offer.Status != oldStatus {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	// Good
	if offer.Hid == 0 {
		offer.Hid = hid
	}
	offer.Status = newStatus
	if offer.Status == bean.OFFER_STATUS_COMPLETED {
		offerProfile := s.getOfferProfile(offer, profile, &ce)
		offerProfile.ActiveOffers[offer.Currency] = false

		transCount := s.getSuccessTransCount(offer)
		err := s.dao.UpdateOfferCompleted(offer, offerProfile, transCount)
		if ce.SetError(api_error.UpdateDataFailed, err) {
			return
		}
	} else {
		err := s.dao.UpdateOffer(offer, offer.GetChangeStatus())
		if ce.SetError(api_error.UpdateDataFailed, err) {
			return
		}
	}

	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) FinishOfferPendingTransfer(ref string) (offer bean.Offer, ce SimpleContextError) {
	to := s.dao.GetOfferByPath(ref)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, to) {
		return
	}
	offer = to.Object.(bean.Offer)

	if offer.Status == bean.OFFER_STATUS_REJECTING {
		offer.Status = bean.OFFER_STATUS_REJECTED
	} else if offer.Status == bean.OFFER_STATUS_CLOSING {
		offer.Status = bean.OFFER_STATUS_CLOSED
	} else if offer.Status == bean.OFFER_STATUS_CANCELLING {
		offer.Status = bean.OFFER_STATUS_CANCELLED
		// Need it here to duplicate solr record for cancelled for both maker and taker
		notification.SendOfferNotification(offer)
		// So the last notification only update for maker
		offer.ToUID = ""
	}

	err := s.dao.UpdateOffer(offer, offer.GetChangeStatus())
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	notification.SendOfferNotification(offer)

	return
}

func (s OfferService) PreShakeOnChainOffer(offerId string, hid int64) (offer bean.Offer, ce SimpleContextError) {
	return s.UpdateOnChainOffer(offerId, hid, bean.OFFER_STATUS_PRE_SHAKING, bean.OFFER_STATUS_PRE_SHAKE)
}

func (s OfferService) ShakeOnChainOffer(offerId string) (offer bean.Offer, ce SimpleContextError) {
	return s.UpdateOnChainOffer(offerId, 0, bean.OFFER_STATUS_SHAKING, bean.OFFER_STATUS_SHAKE)
}

func (s OfferService) RejectOnChainOffer(offerId string) (offer bean.Offer, ce SimpleContextError) {
	return s.UpdateOnChainOffer(offerId, 0, bean.OFFER_STATUS_REJECTING, bean.OFFER_STATUS_REJECTED)
}

func (s OfferService) CompleteOnChainOffer(offerId string) (offer bean.Offer, ce SimpleContextError) {
	return s.UpdateOnChainOffer(offerId, 0, bean.OFFER_STATUS_COMPLETING, bean.OFFER_STATUS_COMPLETED)
}

func (s OfferService) GetQuote(quoteType string, amountStr string, currency string, fiatCurrency string) (price decimal.Decimal, fiatPrice decimal.Decimal,
	fiatAmount decimal.Decimal, err error) {
	amount, numberErr := decimal.NewFromString(amountStr)
	to := dao.MiscDaoInst.GetCurrencyRateFromCache(bean.USD.Code, fiatCurrency)
	if numberErr != nil {
		err = numberErr
	}
	rate := to.Object.(bean.CurrencyRate)
	rateNumber := decimal.NewFromFloat(rate.Rate)
	tmpAmount := amount.Mul(rateNumber)

	if quoteType == "buy" {
		resp, errResp := coinbase_service.GetBuyPrice(currency)
		err = errResp
		if err != nil {
			return
		}
		price = common.StringToDecimal(resp.Amount)
		fiatPrice = price.Mul(rateNumber)
		fiatAmount = tmpAmount.Mul(price)
	} else if quoteType == "sell" {
		resp, errResp := coinbase_service.GetSellPrice(currency)
		err = errResp
		if err != nil {
			return
		}
		price = common.StringToDecimal(resp.Amount)
		fiatPrice = price.Mul(rateNumber)
		fiatAmount = tmpAmount.Mul(price)
	} else {
		err = errors.New(api_error.InvalidQueryParam)
	}

	return
}

func (s OfferService) GetAllQuotes(fiatCurrency string) []interface{} {
	type quoteStruct struct {
		Type         string
		Currency     string
		FiatCurrency string
		// FiatAmount   string
		Price string
	}

	var quoteObj quoteStruct
	quotes := make([]interface{}, 4)

	quoteObj = quoteStruct{
		Type:         bean.OFFER_TYPE_SELL,
		Currency:     bean.BTC.Code,
		FiatCurrency: fiatCurrency,
	}
	_, fiatPrice, _, _ := s.GetQuote(quoteObj.Type, "1", quoteObj.Currency, fiatCurrency)
	quoteObj.Price = fiatPrice.Round(2).String()
	// quote.FiatAmount = fiatAmount.Round(2).String()

	quotes[0] = quoteObj

	quoteObj = quoteStruct{
		Type:         bean.OFFER_TYPE_BUY,
		Currency:     bean.BTC.Code,
		FiatCurrency: fiatCurrency,
	}
	_, fiatPrice, _, _ = s.GetQuote(quoteObj.Type, "1", quoteObj.Currency, fiatCurrency)
	quoteObj.Price = fiatPrice.Round(2).String()
	// quote.FiatAmount = fiatAmount.Round(2).String()

	quotes[1] = quoteObj

	quoteObj = quoteStruct{
		Type:         bean.OFFER_TYPE_SELL,
		Currency:     bean.ETH.Code,
		FiatCurrency: fiatCurrency,
	}
	_, fiatPrice, _, _ = s.GetQuote(quoteObj.Type, "1", quoteObj.Currency, fiatCurrency)
	quoteObj.Price = fiatPrice.Round(2).String()
	// quote.FiatAmount = fiatAmount.Round(2).String()

	quotes[2] = quoteObj

	quoteObj = quoteStruct{
		Type:         bean.OFFER_TYPE_BUY,
		Currency:     bean.ETH.Code,
		FiatCurrency: fiatCurrency,
	}
	_, fiatPrice, _, _ = s.GetQuote(quoteObj.Type, "1", quoteObj.Currency, fiatCurrency)
	quoteObj.Price = fiatPrice.Round(2).String()
	// quote.FiatAmount = fiatAmount.Round(2).String()

	quotes[3] = quoteObj

	return quotes
}

func (s OfferService) FinishOfferConfirmingAddresses() (finishedInstantOffers []bean.Offer, ce SimpleContextError) {
	pendingOffers, err := s.dao.ListOfferConfirmingAddressMap()
	if ce.SetError(api_error.GetDataFailed, err) {
		return
	} else {
		for _, pendingOffer := range pendingOffers {
			bodyTransaction, err := coinbase_service.GetTransaction(pendingOffer.ExternalId, pendingOffer.Currency)
			if err == nil && bodyTransaction.Status == "completed" {
				completed := false
				if pendingOffer.Type == bean.OFFER_ADDRESS_MAP_OFFER {
					offer, ce := s.ActiveOffer(pendingOffer.Address, pendingOffer.Amount)
					if ce.HasError() {
						if ce.StatusKey == api_error.OfferStatusInvalid {
							_, ce = s.UpdateShakeOffer(offer)
							completed = !ce.HasError()
						} else {
							// TODO Need to do some notification if get error
						}
					}
				} else if pendingOffer.Type == bean.OFFER_ADDRESS_MAP_OFFER_STORE {
					_, ce = OfferStoreServiceInst.ActiveOffChainOfferStore(pendingOffer.Address, pendingOffer.Amount, pendingOffer.Currency)
					completed = !ce.HasError()
				} else if pendingOffer.Type == bean.OFFER_ADDRESS_MAP_OFFER_STORE_ITEM {
					_, ce = OfferStoreServiceInst.RefillBalanceOffChainOfferStore(pendingOffer.Address, pendingOffer.Amount, pendingOffer.Currency)
					completed = !ce.HasError()
				} else if pendingOffer.Type == bean.OFFER_ADDRESS_MAP_OFFER_STORE_SHAKE {
					_, ce = OfferStoreServiceInst.PreShakeOffChainOfferStoreShake(pendingOffer.Address, pendingOffer.Amount)
					completed = !ce.HasError()
				}

				if completed {
					dao.OfferDaoInst.RemoveOfferConfirmingAddressMap(pendingOffer.TxHash)
				}
			}
		}
	}

	return
}

func (s OfferService) FinishCryptoTransfer() (finishedInstantOffers []bean.Offer, ce SimpleContextError) {
	pendingOffers, err := s.dao.ListCryptoPendingTransfer()
	if ce.SetError(api_error.GetDataFailed, err) {
		return
	} else {
		for _, pendingOffer := range pendingOffers {
			bodyTransaction, err := coinbase_service.GetTransaction(pendingOffer.ExternalId, pendingOffer.Currency)
			if err == nil && bodyTransaction.Status == "completed" {
				completed := false
				if pendingOffer.DataType == bean.OFFER_ADDRESS_MAP_OFFER {
					_, ce := s.FinishOfferPendingTransfer(pendingOffer.DataRef)
					completed = !ce.HasError()
				} else if pendingOffer.DataType == bean.OFFER_ADDRESS_MAP_OFFER_STORE {
					_, ce = OfferStoreServiceInst.FinishOfferStorePendingTransfer(pendingOffer.DataRef)
					completed = !ce.HasError()
				} else if pendingOffer.DataType == bean.OFFER_ADDRESS_MAP_OFFER_STORE_SHAKE {
					_, ce = OfferStoreServiceInst.FinishOfferStoreShakePendingTransfer(pendingOffer.DataRef)
					completed = !ce.HasError()
				}

				if completed {
					dao.OfferDaoInst.RemoveCryptoPendingTransfer(pendingOffer.TxHash)
				}
			}
		}
	}

	return
}

func (s OfferService) RevertOfferAction(userId string, ref string) (offer bean.Offer, ce SimpleContextError) {
	to := s.dao.GetOfferByPath(ref)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, to) {
		return
	}
	offer = to.Object.(bean.Offer)

	if offer.Status == bean.OFFER_STATUS_CREATED {
		_, ce = s.CloseFailedOffer(userId, offer.Id)
	} else if offer.Status == bean.OFFER_STATUS_PRE_SHAKE {
		_, ce = s.CancelFailedShakeOffer(userId, offer.Id)
	} else {
		_, ce = s.UpdateOfferToPreviousStatus(userId, offer.Id)
	}

	return
}

func (s OfferService) SyncToSolr(offerId string) (offer bean.Offer, ce SimpleContextError) {
	offer = *GetOffer(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	solr_service.UpdateObject(bean.NewSolrFromOffer(offer))

	return
}

func (s OfferService) getSuccessTransCount(offer bean.Offer) bean.TransactionCount {
	transCountTO := s.transDao.GetTransactionCount(offer.UID, offer.Currency)
	var transCount bean.TransactionCount
	if !transCountTO.HasError() {
		transCount = transCountTO.Object.(bean.TransactionCount)
	}
	transCount.Currency = offer.Currency
	transCount.Success += 1

	return transCount
}

func (s OfferService) getFailedTransCount(offer bean.Offer) bean.TransactionCount {
	transCountTO := s.transDao.GetTransactionCount(offer.UID, offer.Currency)
	var transCount bean.TransactionCount
	if !transCountTO.HasError() {
		transCount = transCountTO.Object.(bean.TransactionCount)
	}
	transCount.Currency = offer.Currency
	transCount.Failed += 1

	return transCount
}

func (s OfferService) getOfferProfile(offer bean.Offer, profile bean.Profile, ce *SimpleContextError) (offerProfile bean.Profile) {
	if profile.UserId == offer.UID {
		offerProfile = profile
	} else {
		offerProfileTO := s.userDao.GetProfile(offer.UID)
		if ce.FeedDaoTransfer(api_error.GetDataFailed, offerProfileTO) {
			return
		}
		offerProfile = offerProfileTO.Object.(bean.Profile)
	}

	return
}

func (s OfferService) setupOfferPrice(offer *bean.Offer, ce *SimpleContextError) {
	price, fiatPrice, fiatAmount, err := s.GetQuote(offer.Type, offer.Amount, offer.Currency, offer.FiatCurrency)
	if ce.SetError(api_error.GetDataFailed, err) {
		return
	}

	offer.Price = fiatPrice.Round(2).String()
	offer.PriceUSD = price.Round(2).String()
	offer.PriceNumberUSD, _ = price.Float64()
	offer.PriceNumber, _ = fiatPrice.Float64()
	offer.FiatAmount = fiatAmount.Round(2).String()
}

func (s OfferService) setupOfferAmount(offer *bean.Offer, ce *SimpleContextError) {
	exchFeeTO := s.miscDao.GetSystemFeeFromCache(bean.FEE_KEY_EXCHANGE)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, exchFeeTO) {
		return
	}
	exchFeeObj := exchFeeTO.Object.(bean.SystemFee)
	exchCommTO := s.miscDao.GetSystemFeeFromCache(bean.FEE_KEY_EXCHANGE_COMMISSION)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, exchCommTO) {
		return
	}
	// exchCommObj := exchCommTO.Object.(bean.SystemFee)

	exchFee := decimal.NewFromFloat(exchFeeObj.Value).Round(6)
	// exchComm := decimal.NewFromFloat(exchCommObj.Value).Round(6)
	exchComm := common.Zero
	amount := common.StringToDecimal(offer.Amount)
	fee := amount.Mul(exchFee)
	reward := amount.Mul(exchComm)

	offer.FeePercentage = exchFee.String()
	offer.RewardPercentage = exchComm.String()
	offer.Fee = fee.String()
	offer.Reward = reward.String()
	if offer.IsTypeSell() {
		offer.TotalAmount = amount.Add(fee.Add(reward)).String()
	} else if offer.IsTypeBuy() {
		offer.TotalAmount = amount.Sub(fee.Add(reward)).String()
	}
}

func (s OfferService) transferCrypto(offer *bean.Offer, userId string, ce *SimpleContextError) {
	if offer.Status == bean.OFFER_STATUS_COMPLETED {
		if offer.UserAddress != "" {
			//Transfer
			description := fmt.Sprintf("Transfer to userId %s offerId %s status %s", userId, offer.Id, offer.Status)

			var response1 interface{}
			// var response2 interface{}
			transferAmount := offer.Amount
			if offer.IsTypeBuy() {
				response1 = s.sendTransaction(offer.UserAddress, offer.TotalAmount, offer.Currency, description, offer.Id, *offer, ce)
				transferAmount = offer.TotalAmount
			} else {
				response1 = s.sendTransaction(offer.UserAddress, offer.Amount, offer.Currency, description, offer.Id, *offer, ce)
			}
			if ce.HasError() {
				return
			}
			s.miscDao.AddCryptoTransferLog(bean.CryptoTransferLog{
				Provider:         offer.WalletProvider,
				ProviderResponse: response1,
				DataType:         bean.OFFER_ADDRESS_MAP_OFFER,
				DataRef:          dao.GetOfferItemPath(offer.Id),
				UID:              userId,
				Description:      description,
				Amount:           transferAmount,
				Currency:         offer.Currency,
			})

			// Transfer reward
			//if offer.RewardAddress != "" {
			//	rewardDescription := fmt.Sprintf("Transfer reward to userId %s offerId %s", userId, offer.Id)
			//	response2 = s.sendTransaction(offer.RewardAddress, offer.Reward, offer.Currency, rewardDescription,
			//		fmt.Sprintf("%s_reward", offer.Id), *offer, ce)
			//}
			// Just logging the error, don't throw it
			//if ce.HasError() {
			//	return
			//}
			offer.Provider = bean.OFFER_PROVIDER_COINBASE
			offer.ProviderData = response1
			//externalId = coinbaseResponse.Id
		} else {
			ce.SetStatusKey(api_error.InvalidRequestBody)
			return
		}
	} else if offer.Status == bean.OFFER_STATUS_REJECTED || offer.Status == bean.OFFER_STATUS_CLOSED || offer.Status == bean.OFFER_STATUS_CANCELLED {
		if offer.RefundAddress != "" {
			//Refund
			var response interface{}
			transferAmount := offer.Amount
			description := fmt.Sprintf("Refund to userId %s offerId %s status %s", userId, offer.Id, offer.Status)
			if offer.IsTypeBuy() {
				response = s.sendTransaction(offer.RefundAddress, offer.Amount, offer.Currency, description, offer.Id, *offer, ce)
			} else {
				response = s.sendTransaction(offer.RefundAddress, offer.TotalAmount, offer.Currency, description, offer.Id, *offer, ce)
				transferAmount = offer.TotalAmount
			}
			if ce.HasError() {
				return
			}
			s.miscDao.AddCryptoTransferLog(bean.CryptoTransferLog{
				Provider:         offer.WalletProvider,
				ProviderResponse: response,
				DataType:         bean.OFFER_ADDRESS_MAP_OFFER,
				DataRef:          dao.GetOfferItemPath(offer.Id),
				UID:              userId,
				Description:      description,
				Amount:           transferAmount,
				Currency:         offer.Currency,
			})
			offer.Provider = bean.OFFER_PROVIDER_COINBASE
			offer.ProviderData = response
		} else {
			ce.SetStatusKey(api_error.InvalidRequestBody)
			return
		}
	}
}

func (s OfferService) generateSystemAddress(offer *bean.Offer, ce *SimpleContextError) {
	// Only BTC need to generate address to transfer in
	if offer.Currency == bean.BTC.Code {
		systemConfigTO := s.miscDao.GetSystemConfigFromCache(bean.CONFIG_BTC_WALLET)
		if ce.FeedDaoTransfer(api_error.GetDataFailed, systemConfigTO) {
			return
		}
		systemConfig := systemConfigTO.Object.(bean.SystemConfig)

		if systemConfig.Value == bean.BTC_WALLET_COINBASE {
			addressResponse, err := coinbase_service.GenerateAddress(offer.Currency)
			if err != nil {
				ce.SetError(api_error.ExternalApiFailed, err)
				return
			}
			offer.SystemAddress = addressResponse.Data.Address
			offer.WalletProvider = systemConfig.Value
		} else if systemConfig.Value == bean.BTC_WALLET_BLOCKCHAINIO {
			client := blockchainio_service.BlockChainIOClient{}
			address, err := client.GenerateAddress(offer.Id)
			if err != nil {
				ce.SetError(api_error.ExternalApiFailed, err)
				return
			}
			offer.SystemAddress = address
			offer.WalletProvider = systemConfig.Value
		} else {
			ce.SetStatusKey(api_error.InvalidConfig)
		}
	}
}

func (s OfferService) sendTransaction(address string, amountStr string, currency string, description string, withdrawId string,
	offer bean.Offer, ce *SimpleContextError) interface{} {
	// Only BTC
	if currency == bean.BTC.Code {

		if offer.WalletProvider == bean.BTC_WALLET_COINBASE {
			response, err := coinbase_service.SendTransaction(address, amountStr, currency, description, withdrawId)
			if ce.SetError(api_error.ExternalApiFailed, err) {
				return ""
			}
			return response
		} else if offer.WalletProvider == bean.BTC_WALLET_BLOCKCHAINIO {
			client := blockchainio_service.BlockChainIOClient{}
			amount := common.StringToDecimal(amountStr)
			hashTx, err := client.SendTransaction(address, amount)
			if ce.SetError(api_error.ExternalApiFailed, err) {
				return ""
			}
			return hashTx
		} else {
			ce.SetStatusKey(api_error.InvalidConfig)
		}
	}

	return ""
}
