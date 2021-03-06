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
	"github.com/ninjadotorg/handshake-exchange/integration/ethereum_service"
	"github.com/ninjadotorg/handshake-exchange/integration/exchangehandshakeshop_service"
	"github.com/ninjadotorg/handshake-exchange/integration/solr_service"
	"github.com/ninjadotorg/handshake-exchange/service/notification"
	"github.com/shopspring/decimal"
	"strconv"
	"strings"
	"time"
)

type OfferStoreService struct {
	dao      *dao.OfferStoreDao
	userDao  *dao.UserDao
	miscDao  *dao.MiscDao
	transDao *dao.TransactionDao
	offerDao *dao.OfferDao
}

func (s OfferStoreService) CreateOfferStore(userId string, offerSetup bean.OfferStoreSetup) (offer bean.OfferStoreSetup, ce SimpleContextError) {
	offerBody := offerSetup.Offer
	offerItemBody := offerSetup.Item

	// Check offer store exists
	// Allow to re-create if offer store exist but all item is closed
	offerTO := s.dao.GetOfferStore(userId)
	if offerTO.Found {
		offerCheck := offerTO.Object.(bean.OfferStore)
		allFalse := true
		for _, v := range offerCheck.ItemFlags {
			if v == true {
				allFalse = false
				break
			}
		}
		if !allFalse {
			ce.SetStatusKey(api_error.OfferStoreExists)
			return
		}
	}

	profile := GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}

	s.prepareOfferStore(&offerBody, &offerItemBody, profile, &ce)
	if ce.HasError() {
		return
	}
	if offerItemBody.FreeStart != "" {
		s.registerFreeStart(userId, &offerItemBody, &ce)
		if ce.HasError() {
			return
		}
	}

	offerNew, err := s.dao.AddOfferStore(offerBody, offerItemBody, *profile)
	if ce.SetError(api_error.AddDataFailed, err) {
		return
	}

	offerNew.CreatedAt = time.Now().UTC()
	offerNew.ItemSnapshots = offerBody.ItemSnapshots
	notification.SendOfferStoreNotification(offerNew, offerItemBody)

	offer.Offer = offerNew
	offer.Item = offerItemBody

	// Everything done, call contract
	if offerItemBody.FreeStart != "" {
		// Only ETH
		if offerItemBody.Currency == bean.ETH.Code {
			client := exchangehandshakeshop_service.ExchangeHandshakeShopClient{}
			sellAmount := common.StringToDecimal(offerItemBody.SellTotalAmount)
			txHash, onChainErr := client.InitByShopOwner(offerNew.Id, sellAmount)
			if onChainErr != nil {
				fmt.Println(onChainErr)
			}
			fmt.Println(txHash)
		}
	}

	return
}

func (s OfferStoreService) GetOfferStore(userId string, offerId string) (offer bean.OfferStore, ce SimpleContextError) {
	GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}

	offerTO := s.dao.GetOfferStore(offerId)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, offerTO) {
		return
	}
	notFound := false
	if offerTO.Found {
		offer = offerTO.Object.(bean.OfferStore)
		allFalse := true
		for _, v := range offer.ItemFlags {
			if v == true {
				allFalse = false
				break
			}
		}
		if allFalse {
			notFound = true
		}
	} else {
		notFound = true
	}

	if notFound {
		ce.NotFound = true
	}

	return
}

func (s OfferStoreService) AddOfferStoreItem(userId string, offerId string, item bean.OfferStoreItem) (offer bean.OfferStore, ce SimpleContextError) {
	profile := GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}

	s.prepareOfferStore(&offer, &item, profile, &ce)
	if ce.HasError() {
		return
	}
	if item.FreeStart != "" {
		s.registerFreeStart(userId, &item, &ce)
		if ce.HasError() {
			return
		}
	}

	_, err := s.dao.AddOfferStoreItem(offer, item, *profile)
	if ce.SetError(api_error.AddDataFailed, err) {
		return
	}

	notification.SendOfferStoreNotification(offer, item)

	// Everything done, call contract
	if item.FreeStart != "" {
		// Only ETH
		if item.Currency == bean.ETH.Code {
			client := exchangehandshakeshop_service.ExchangeHandshakeShopClient{}
			sellAmount := common.StringToDecimal(item.SellTotalAmount)
			txHash, onChainErr := client.InitByShopOwner(offer.Id, sellAmount)
			if onChainErr != nil {
				fmt.Println(onChainErr)
			}
			fmt.Println(txHash)
		}
	}

	return
}

func (s OfferStoreService) UpdateOfferStore(userId string, offerId string, body bean.OfferStoreSetup) (offer bean.OfferStore, ce SimpleContextError) {
	_ = GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	checkOffer := GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	offer = *checkOffer
	bodyItem := body.Item
	checkOfferItem := GetOfferStoreItem(*s.dao, offerId, bodyItem.Currency, &ce)
	if ce.HasError() {
		return
	}
	// Copy data
	offer.FiatCurrency = body.Offer.FiatCurrency
	offer.ContactPhone = body.Offer.ContactPhone
	offer.ContactInfo = body.Offer.ContactInfo
	item := *checkOfferItem
	if bodyItem.SellPercentage != "" {
		// Convert to 0.0x
		percentage, errFmt := decimal.NewFromString(bodyItem.SellPercentage)
		if ce.SetError(api_error.InvalidRequestBody, errFmt) {
			return
		}
		item.SellPercentage = percentage.Div(decimal.NewFromFloat(100)).String()
	} else {
		item.SellPercentage = "0"
	}

	if bodyItem.BuyPercentage != "" {
		// Convert to 0.0x
		percentage, errFmt := decimal.NewFromString(bodyItem.BuyPercentage)
		if ce.SetError(api_error.InvalidRequestBody, errFmt) {
			return
		}
		item.BuyPercentage = percentage.Div(decimal.NewFromFloat(100)).String()
	} else {
		item.BuyPercentage = "0"
	}
	offer.ItemSnapshots[bodyItem.Currency] = item

	_, err := s.dao.UpdateOfferStoreItem(offer, item)
	if ce.SetError(api_error.AddDataFailed, err) {
		return
	}

	// Only sync to solr
	solr_service.UpdateObject(bean.NewSolrFromOfferStore(offer, item))

	return
}

func (s OfferStoreService) RefillOfferStoreItem(userId string, offerId string, body bean.OfferStoreItem) (offer bean.OfferStore, ce SimpleContextError) {
	_ = GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	item := *GetOfferStoreItem(*s.dao, offerId, body.Currency, &ce)
	if ce.HasError() {
		return
	}
	if item.FreeStart != "" {
		ce.SetStatusKey(api_error.InvalidRequestBody)
		return
	}
	if item.SubStatus == bean.OFFER_STORE_ITEM_STATUS_REFILLING {
		ce.SetStatusKey(api_error.InvalidRequestBody)
		return
	}

	s.prepareRefillOfferStoreItem(&offer, &item, &body, &ce)
	if ce.HasError() {
		return
	}

	_, err := s.dao.UpdateRefillOfferStoreItem(offer, item)
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}
	// Only update buy first
	err = s.dao.RefillBalanceOfferStoreItem(offer, &item, body, bean.OFFER_TYPE_BUY)
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}
	// Only sync to solr and notification firebase
	solr_service.UpdateObject(bean.NewSolrFromOfferStore(offer, item))
	dao.OfferStoreDaoInst.UpdateNotificationOfferStoreItem(offer, item)
	offer.ItemSnapshots[item.Currency] = item

	return
}

func (s OfferStoreService) RemoveOfferStoreItem(userId string, offerId string, currency string) (offer bean.OfferStore, ce SimpleContextError) {
	profile := GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	item := *GetOfferStoreItem(*s.dao, offerId, currency, &ce)
	if ce.HasError() {
		return
	}

	if item.Status != bean.OFFER_STORE_ITEM_STATUS_ACTIVE && item.Status != bean.OFFER_STORE_ITEM_STATUS_CLOSING {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	count, err := s.countActiveShake(offer.Id, "", item.Currency)
	if err != nil {
		ce.SetError(api_error.GetDataFailed, err)
		return
	}
	// There is still active shake so cannot close
	if count > 0 {
		ce.SetStatusKey(api_error.OfferStoreShakeActiveExist)
		return
	}

	hasSell := false
	sellAmount := common.StringToDecimal(item.SellAmount)
	sellBalance := common.StringToDecimal(item.SellBalance)
	if sellAmount.GreaterThan(common.Zero) {
		hasSell = true
		// Only ETH
		if item.Currency == bean.ETH.Code {
			activeCount, _ := s.countActiveShake(offer.Id, item.Currency, item.Currency)
			if err != nil {
				ce.SetError(api_error.GetDataFailed, err)
			}
			// There is no active sell for ETH anymore so just close it
			if activeCount == 0 && sellBalance.Equal(common.Zero) {
				hasSell = false
			}
		}
	}

	// Only BTC, refund the crypto
	if item.Currency != bean.ETH.Code {
		// Do Refund
		if hasSell {
			description := fmt.Sprintf("Refund to userId %s due to close the offer", userId)
			response := s.sendTransaction(item.UserAddress,
				item.SellBalance, item.Currency, description, offer.UID, item.WalletProvider, &ce)
			if !ce.HasError() {
				s.miscDao.AddCryptoTransferLog(bean.CryptoTransferLog{
					Provider:         item.WalletProvider,
					ProviderResponse: response,
					DataType:         bean.OFFER_ADDRESS_MAP_OFFER_STORE,
					DataRef:          dao.GetOfferStoreItemPath(offerId),
					UID:              userId,
					Description:      description,
					Amount:           item.SellBalance,
					Currency:         item.Currency,
				})
			}
		}
	}

	allFalse := true
	// Just for check
	offer.ItemFlags[item.Currency] = false
	for _, v := range offer.ItemFlags {
		if v == true {
			allFalse = false
			break
		}
	}
	waitOnChain := (item.Currency == bean.ETH.Code && hasSell) || offer.Status == bean.OFFER_STORE_STATUS_CLOSING
	if allFalse {
		if waitOnChain {
			// Need to wait for OnChain
			offer.Status = bean.OFFER_STORE_STATUS_CLOSING
		} else {
			offer.Status = bean.OFFER_STORE_STATUS_CLOSED
		}
	}

	// Just a time to response
	item.UpdatedAt = time.Now().UTC()
	if waitOnChain {
		// Only update
		item.Status = bean.OFFER_STORE_ITEM_STATUS_CLOSING
		offer.ItemSnapshots[item.Currency] = item

		err := s.dao.UpdateOfferStoreItemClosing(offer, item)
		if ce.SetError(api_error.UpdateDataFailed, err) {
			return
		}
	} else {
		profile.ActiveOfferStores[item.Currency] = false
		offer.ItemFlags = profile.ActiveOfferStores

		// Really remove the item
		item.Status = bean.OFFER_STORE_ITEM_STATUS_CLOSED
		offer.ItemSnapshots[item.Currency] = item

		err := s.dao.RemoveOfferStoreItem(offer, item, *profile)
		if ce.SetError(api_error.DeleteDataFailed, err) {
			return
		}
	}

	// Assign to correct flag
	offer.ItemFlags[item.Currency] = item.Status != bean.OFFER_STORE_ITEM_STATUS_CLOSED

	notification.SendOfferStoreNotification(offer, item)

	// Everything done, call contract
	if item.FreeStart != "" {
		// Only ETH
		s.dao.UpdateOfferStoreFreeStartUserUsing(profile.UserId)
		if item.Currency == bean.ETH.Code && waitOnChain {
			client := exchangehandshakeshop_service.ExchangeHandshakeShopClient{}
			txHash, onChainErr := client.CloseByShopOwner(offer.Id, offer.Hid)
			if onChainErr != nil {
				fmt.Println(onChainErr)
			}
			fmt.Println(txHash)
		}
	}

	return
}

func (s OfferStoreService) RemoveFailedOfferStoreItem(userId string, offerId string, currency string) (offer bean.OfferStore, ce SimpleContextError) {
	profile := GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	item := GetOfferStoreItem(*s.dao, offerId, currency, &ce)
	if ce.HasError() {
		return
	}

	if item.Status != bean.OFFER_STORE_ITEM_STATUS_CREATED {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	allFalse := true
	// Just for check
	offer.ItemFlags[item.Currency] = false
	for _, v := range offer.ItemFlags {
		if v == true {
			allFalse = false
			break
		}
	}
	if allFalse {
		offer.Status = bean.OFFER_STORE_STATUS_CLOSED
	}

	profile.ActiveOfferStores[item.Currency] = false
	offer.ItemFlags = profile.ActiveOfferStores

	// Really remove the item
	item.Status = bean.OFFER_STORE_ITEM_STATUS_CLOSED
	offer.ItemSnapshots[item.Currency] = *item

	err := s.dao.RemoveOfferStoreItem(offer, *item, *profile)
	if ce.SetError(api_error.DeleteDataFailed, err) {
		return
	}

	// Assign to correct flag
	offer.ItemFlags[item.Currency] = item.Status != bean.OFFER_STORE_ITEM_STATUS_CLOSED
	// Only sync to solr
	solr_service.UpdateObject(bean.NewSolrFromOfferStore(offer, *item))

	return
}

func (s OfferStoreService) CancelRefillOfferStoreItem(userId string, offerId string, currency string) (offer bean.OfferStore, ce SimpleContextError) {
	_ = GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	item := GetOfferStoreItem(*s.dao, offerId, currency, &ce)
	if ce.HasError() {
		return
	}

	if item.SubStatus != bean.OFFER_STORE_ITEM_STATUS_REFILLING {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	item.SubStatus = bean.OFFER_STORE_ITEM_STATUS_UNDO_REFILL
	item.SellAmount = item.SellBackupAmounts["sell_amount"].(string)
	item.SellTotalAmount = item.SellBackupAmounts["sell_total_amount"].(string)
	offer.ItemSnapshots[item.Currency] = *item

	_, err := s.dao.UpdateCancelRefillOfferStoreItem(offer, *item)
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	// Only sync to solr and notification firebase
	solr_service.UpdateObject(bean.NewSolrFromOfferStore(offer, *item))
	dao.OfferStoreDaoInst.UpdateNotificationOfferStoreItem(offer, *item)

	return
}

func (s OfferStoreService) OpenCloseFailedOfferStore(userId, offerId string, currency string) (offer bean.OfferStore, ce SimpleContextError) {
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	item := *GetOfferStoreItem(*s.dao, offerId, currency, &ce)
	if ce.HasError() {
		return
	}
	if item.Status != bean.OFFER_STORE_ITEM_STATUS_CLOSING {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	item.Status = bean.OFFER_STORE_ITEM_STATUS_ACTIVE
	offer.ItemFlags[item.Currency] = item.Status != bean.OFFER_STORE_ITEM_STATUS_CLOSED
	if offer.Status == bean.OFFER_STORE_STATUS_CLOSING {
		offer.Status = bean.OFFER_STORE_STATUS_ACTIVE
	}
	offer.ItemSnapshots[item.Currency] = item
	err := s.dao.UpdateOfferStoreItemActive(offer, item)
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}
	// Only sync to solr
	solr_service.UpdateObject(bean.NewSolrFromOfferStore(offer, item))

	return
}

func (s OfferStoreService) OnChainOfferStoreTracking(userId string, offerId string, body bean.OfferOnChainTransaction) (offer bean.OfferStore, ce SimpleContextError) {
	profile := *GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}

	onChainTracking := bean.OfferOnChainActionTracking{
		UID:      profile.UserId,
		Offer:    offer.Id,
		OfferRef: dao.GetOfferStoreItemPath(offer.Id),
		Type:     bean.OFFER_ADDRESS_MAP_OFFER_STORE,
		TxHash:   body.TxHash,
		Currency: body.Currency,
		Action:   body.Action,
		Reason:   body.Reason,
	}

	err := s.offerDao.AddOfferOnChainActionTracking(onChainTracking)
	if ce.SetError(api_error.AddDataFailed, err) {
		return
	}

	return
}

func (s OfferStoreService) OnChainOfferStoreItemTracking(userId string, offerId string, body bean.OfferOnChainTransaction) (offer bean.OfferStore, ce SimpleContextError) {
	profile := *GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}

	onChainTracking := bean.OfferOnChainActionTracking{
		UID:      profile.UserId,
		Offer:    offer.Id,
		OfferRef: dao.GetOfferStoreItemItemPath(offer.Id, body.Currency),
		Type:     bean.OFFER_ADDRESS_MAP_OFFER_STORE_ITEM,
		TxHash:   body.TxHash,
		Currency: body.Currency,
		Action:   body.Action,
		Reason:   body.Reason,
	}

	err := s.offerDao.AddOfferOnChainActionTracking(onChainTracking)
	if ce.SetError(api_error.AddDataFailed, err) {
		return
	}

	return
}

func (s OfferStoreService) CreateOfferStoreShake(userId string, offerId string, offerShakeBody bean.OfferStoreShake) (offerShake bean.OfferStoreShake, ce SimpleContextError) {
	profile := GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	if UserServiceInst.CheckOfferLocked(*profile) {
		ce.SetStatusKey(api_error.OfferActionLocked)
		return
	}

	offer := *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	if profile.UserId == offer.UID {
		ce.SetStatusKey(api_error.OfferPayMyself)
		return
	}

	item := *GetOfferStoreItem(*s.dao, offerId, offerShakeBody.Currency, &ce)
	if ce.HasError() {
		return
	}

	// Make sure shake on the valid item
	if item.Status != bean.OFFER_STORE_ITEM_STATUS_ACTIVE {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
	}
	var balance decimal.Decimal
	amount := common.StringToDecimal(offerShakeBody.Amount)
	if offerShakeBody.Currency == bean.ETH.Code {
		if amount.LessThan(bean.MIN_ETH) {
			ce.SetStatusKey(api_error.AmountIsTooSmall)
			return
		}
	}
	if offerShakeBody.Currency == bean.BTC.Code {
		if amount.LessThan(bean.MIN_BTC) {
			ce.SetStatusKey(api_error.AmountIsTooSmall)
			return
		}
	}
	if offerShakeBody.Currency == bean.BCH.Code {
		if amount.LessThan(bean.MIN_BCH) {
			ce.SetStatusKey(api_error.AmountIsTooSmall)
			return
		}
	}

	// Total usage from current usage shake, so it's more accuracy than need to wait real balance update
	usageBalance, err := s.getUsageBalance(offerId, offerShakeBody.Type, offerShakeBody.Currency)
	if err != nil {
		ce.SetError(api_error.GetDataFailed, err)
		return
	}
	if offerShakeBody.IsTypeSell() {
		balance = common.StringToDecimal(item.SellBalance)
	} else {
		balance = common.StringToDecimal(item.BuyBalance)
	}
	if balance.LessThan(usageBalance.Add(amount)) {
		ce.SetStatusKey(api_error.OfferStoreNotEnoughBalance)
	}

	offerShakeBody.UID = userId
	offerShakeBody.FiatCurrency = offer.FiatCurrency
	offerShakeBody.Latitude = offer.Latitude
	offerShakeBody.Longitude = offer.Longitude
	offerShakeBody.FreeStart = item.FreeStart

	s.setupOfferShakePrice(&offerShakeBody, &ce)
	s.setupOfferShakeAmount(&offerShakeBody, &ce)
	if ce.HasError() {
		return
	}

	// Status of shake
	if offerShakeBody.IsTypeSell() {
		// SHAKE
		offerShakeBody.Status = bean.OFFER_STORE_SHAKE_STATUS_SHAKE
		err = s.dao.UpdateOfferStoreShakeBalance(offer, &item, offerShakeBody, true)
		offer.ItemSnapshots[item.Currency] = item
		if ce.SetError(api_error.UpdateDataFailed, err) {
			return
		}
		s.updatePendingTransCount(offer, offerShakeBody, userId)
	} else {
		if offerShakeBody.Currency == bean.ETH.Code {
			offerShakeBody.Status = bean.OFFER_STORE_SHAKE_STATUS_PRE_SHAKING
		} else {
			offerShakeBody.Status = bean.OFFER_STORE_SHAKE_STATUS_SHAKING
			s.generateSystemAddressForShake(offer, &offerShakeBody, &ce)
			if ce.HasError() {
				return
			}
		}
	}

	offerShake, err = s.dao.AddOfferStoreShake(offer, offerShakeBody)
	if ce.SetError(api_error.AddDataFailed, err) {
		return
	}

	offerShake.CreatedAt = time.Now().UTC()
	notification.SendOfferStoreShakeNotification(offerShake, offer)
	notification.SendOfferStoreNotification(offer, item)

	return
}

func (s OfferStoreService) RejectOfferStoreShake(userId string, offerId string, offerShakeId string) (offerShake bean.OfferStoreShake, ce SimpleContextError) {
	profile := *GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer := *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	offerShake = *GetOfferStoreShake(*s.dao, offerId, offerShakeId, &ce)
	if ce.HasError() {
		return
	}
	item := *GetOfferStoreItem(*s.dao, offerId, offerShake.Currency, &ce)
	if ce.HasError() {
		return
	}

	if profile.UserId != offer.UID && profile.UserId != offerShake.UID {
		ce.SetStatusKey(api_error.InvalidRequestBody)
		return
	}
	if offerShake.Status != bean.OFFER_STORE_SHAKE_STATUS_SHAKE {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	if offerShake.Type == bean.OFFER_TYPE_SELL {
		offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_REJECTED
		// REJECTED
		err := s.dao.UpdateOfferStoreShakeBalance(offer, &item, offerShake, false)
		offer.ItemSnapshots[item.Currency] = item
		if ce.SetError(api_error.UpdateDataFailed, err) {
			return
		}
		// Special for free start
		if item.FreeStart != "" {
			s.dao.UpdateOfferStoreFreeStartUserUsing(profile.UserId)
		}

		s.updateFailedTransCount(offer, offerShake, userId)
	} else {
		if offerShake.Currency == bean.ETH.Code {
			// Only ETH
			offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_REJECTING
		} else {
			// Only BTC
			offerStoreItemTO := s.dao.GetOfferStoreItem(userId, offerShake.Currency)
			if offerStoreItemTO.HasError() {
				ce.SetStatusKey(api_error.GetDataFailed)
				return
			}
			offerStoreItem := offerStoreItemTO.Object.(bean.OfferStoreItem)

			offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_REJECTED
			description := fmt.Sprintf("Refund to userId %s due to reject the offer", offerShake.UID)
			userAddress := offerShake.UserAddress

			response := s.sendTransaction(userAddress,
				offerShake.Amount, offerShake.Currency, description, offer.UID, offerStoreItem.WalletProvider, &ce)
			if !ce.HasError() {
				s.miscDao.AddCryptoTransferLog(bean.CryptoTransferLog{
					Provider:         offerStoreItem.WalletProvider,
					ProviderResponse: response,
					DataType:         bean.OFFER_ADDRESS_MAP_OFFER_STORE_SHAKE,
					DataRef:          dao.GetOfferStoreShakeItemPath(offerId, offerShakeId),
					UID:              userId,
					Description:      description,
					Amount:           offerShake.Amount,
					Currency:         offerShake.Currency,
				})
			}

			s.updateFailedTransCount(offer, offerShake, userId)
		}
	}

	err := s.dao.UpdateOfferStoreShakeReject(offer, offerShake, profile)
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	if userId == offerShake.UID {
		UserServiceInst.UpdateOfferRejectLock(profile)
	}

	offerShake.ActionUID = userId
	notification.SendOfferStoreShakeNotification(offerShake, offer)
	notification.SendOfferStoreNotification(offer, item)

	return
}

func (s OfferStoreService) CancelOfferStoreShake(userId string, offerId string, offerShakeId string) (offerShake bean.OfferStoreShake, ce SimpleContextError) {
	profile := GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer := *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	offerShake = *GetOfferStoreShake(*s.dao, offerId, offerShakeId, &ce)
	if ce.HasError() {
		return
	}

	if profile.UserId != offer.UID && profile.UserId != offerShake.UID {
		ce.SetStatusKey(api_error.InvalidRequestBody)
		return
	}
	if offerShake.Status != bean.OFFER_STORE_SHAKE_STATUS_PRE_SHAKE {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
	}

	if offerShake.Currency == bean.ETH.Code {
		// Only ETH
		offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_CANCELLING
	} else {
		// Only BTC
		offerStoreItemTO := s.dao.GetOfferStoreItem(userId, offerShake.Currency)
		if offerStoreItemTO.HasError() {
			ce.SetStatusKey(api_error.GetDataFailed)
			return
		}
		offerStoreItem := offerStoreItemTO.Object.(bean.OfferStoreItem)

		offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_CANCELLED
		description := fmt.Sprintf("Refund to userId %s due to reject the offer", offerShake.UID)
		userAddress := offerShake.UserAddress
		transferAmount := offerShake.Amount
		if offerShake.Type == bean.OFFER_TYPE_BUY {
			description = fmt.Sprintf("Refund to userId %s due to reject the offer", offer.UID)
			userAddress = offerStoreItem.UserAddress
			transferAmount = offerShake.TotalAmount
		}
		response := s.sendTransaction(userAddress,
			transferAmount, offerShake.Currency, description, offer.UID, offerStoreItem.WalletProvider, &ce)
		if !ce.HasError() {
			s.miscDao.AddCryptoTransferLog(bean.CryptoTransferLog{
				Provider:         offerStoreItem.WalletProvider,
				ProviderResponse: response,
				DataType:         bean.OFFER_ADDRESS_MAP_OFFER_STORE_SHAKE,
				DataRef:          dao.GetOfferStoreShakeItemPath(offerId, offerShakeId),
				UID:              userId,
				Description:      description,
				Amount:           transferAmount,
				Currency:         offerShake.Currency,
			})
		}
	}

	err := s.dao.UpdateOfferStoreShake(offerId, offerShake, offerShake.GetChangeStatus())
	if err != nil {
		ce.SetError(api_error.UpdateDataFailed, err)
		return
	}
	notification.SendOfferStoreShakeNotification(offerShake, offer)

	return
}

func (s OfferStoreService) AcceptOfferStoreShake(userId string, offerId string, offerShakeId string) (offerShake bean.OfferStoreShake, ce SimpleContextError) {
	profile := GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer := *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}

	offerShake = *GetOfferStoreShake(*s.dao, offerId, offerShakeId, &ce)
	if ce.HasError() {
		return
	}
	item := *GetOfferStoreItem(*s.dao, offerId, offerShake.Currency, &ce)
	if ce.HasError() {
		return
	}

	if profile.UserId != offer.UID {
		ce.SetStatusKey(api_error.InvalidRequestBody)
		return
	}
	if offerShake.Status != bean.OFFER_STORE_SHAKE_STATUS_PRE_SHAKE {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
	}

	// Now accept always to SHAKE
	offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_SHAKE
	err := s.dao.UpdateOfferStoreShakeBalance(offer, &item, offerShake, true)
	if err == nil {
		s.updatePendingTransCount(offer, offerShake, offer.UID)
	} else {
		return
	}

	err = s.dao.UpdateOfferStoreShake(offerId, offerShake, offerShake.GetChangeStatus())
	if err != nil {
		ce.SetError(api_error.UpdateDataFailed, err)
		return
	}
	notification.SendOfferStoreShakeNotification(offerShake, offer)

	return
}

func (s OfferStoreService) CompleteOfferStoreShake(userId string, offerId string, offerShakeId string) (offerShake bean.OfferStoreShake, ce SimpleContextError) {
	profile := GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer := *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	offerShake = *GetOfferStoreShake(*s.dao, offerId, offerShakeId, &ce)
	if ce.HasError() {
		return
	}
	item := *GetOfferStoreItem(*s.dao, offerId, offerShake.Currency, &ce)
	if ce.HasError() {
		return
	}

	if offerShake.Type == bean.OFFER_TYPE_SELL {
		if profile.UserId != offer.UID {
			ce.SetStatusKey(api_error.InvalidRequestBody)
			return
		}
	} else {
		if profile.UserId != offerShake.UID {
			ce.SetStatusKey(api_error.InvalidRequestBody)
			return
		}
	}

	if offerShake.Status != bean.OFFER_STORE_SHAKE_STATUS_SHAKE {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
	}

	if offerShake.Currency == bean.ETH.Code {
		// Only ETH
		offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_COMPLETING
	} else {
		// Only BTC
		offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_COMPLETED
		// Do Transfer
		s.transferCrypto(&offer, &offerShake, &ce)
		if ce.HasError() {
			return
		}
		s.updateSuccessTransCount(offer, offerShake, userId)
	}

	err := s.dao.UpdateOfferStoreShakeComplete(offer, offerShake, *profile)

	if err != nil {
		ce.SetError(api_error.UpdateDataFailed, err)
		return
	}

	// For onchain processing
	if offerShake.Hid == 0 {
		offerShake.Hid = offer.Hid
	}
	if offerShake.UserAddress == "" {
		offerShake.UserAddress = offer.ItemSnapshots[offerShake.Currency].UserAddress
	}
	notification.SendOfferStoreShakeNotification(offerShake, offer)

	// Everything done, call contract
	if item.FreeStart != "" {
		// Only ETH
		s.dao.UpdateOfferStoreFreeStartUserDone(profile.UserId)
		if item.Currency == bean.ETH.Code && profile.UserId == offer.UID {
			client := exchangehandshakeshop_service.ExchangeHandshakeShopClient{}
			amount := common.StringToDecimal(offerShake.Amount)
			txHash, onChainErr := client.ReleasePartialFund(offerShake.OffChainId, offer.Hid, offer.UID, amount, offerShake.UserAddress)
			if onChainErr != nil {
				fmt.Println(onChainErr)
			}
			fmt.Println(txHash)
		}
	}

	return
}

func (s OfferStoreService) UpdateOfferShakeToPreviousStatus(userId string, offerShakeId string) (offerShake bean.OfferStoreShake, ce SimpleContextError) {
	offer := *GetOfferStore(*s.dao, userId, &ce)
	if ce.HasError() {
		return
	}
	offerShake = *GetOfferStoreShake(*s.dao, userId, offerShakeId, &ce)
	if ce.HasError() {
		return
	}
	if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_SHAKING {
		if offerShake.Currency == bean.ETH.Code {
			offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_PRE_SHAKE
		} else {
			offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_CANCELLED
		}
	} else if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_PRE_SHAKING {
		offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_CANCELLED
	} else if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_CANCELLING {
		offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_PRE_SHAKE
	} else if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_REJECTING {
		offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_SHAKE
	} else if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_COMPLETING {
		offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_SHAKE
	} else {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
	}

	err := s.dao.UpdateOfferStoreShake(userId, offerShake, offerShake.GetChangeStatus())
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}
	notification.SendOfferStoreShakeNotification(offerShake, offer)

	return
}

func (s OfferStoreService) OnChainOfferStoreShakeTracking(userId string, offerId string, offerShakeId string, body bean.OfferOnChainTransaction) (offerShake bean.OfferStoreShake, ce SimpleContextError) {
	profile := GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	offerShake = *GetOfferStoreShake(*s.dao, offerId, offerShakeId, &ce)
	if ce.HasError() {
		return
	}

	onChainTracking := bean.OfferOnChainActionTracking{
		UID:      profile.UserId,
		Offer:    offerShake.Id,
		OfferRef: dao.GetOfferStoreShakeItemPath(offerId, offerShakeId),
		Type:     bean.OFFER_ADDRESS_MAP_OFFER_STORE_SHAKE,
		TxHash:   body.TxHash,
		Currency: body.Currency,
		Action:   body.Action,
		Reason:   body.Reason,
	}

	err := s.offerDao.AddOfferOnChainActionTracking(onChainTracking)
	if ce.SetError(api_error.AddDataFailed, err) {
		return
	}

	return
}

func (s OfferStoreService) UpdateOnChainInitOfferStore(offerId string, hid int64, currency string) (offer bean.OfferStore, ce SimpleContextError) {
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	item := *GetOfferStoreItem(*s.dao, offerId, currency, &ce)
	if ce.HasError() {
		return
	}

	fmt.Println(item.Status)
	fmt.Println(item.SubStatus)
	if item.Status == bean.OFFER_STORE_ITEM_STATUS_CREATED ||
		(item.Status == bean.OFFER_STORE_ITEM_STATUS_ACTIVE && item.SubStatus == bean.OFFER_STORE_ITEM_STATUS_REFILLING) {
	} else {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	// Good
	if offer.Hid == 0 {
		offer.Hid = hid
	}
	item.Status = bean.OFFER_STORE_ITEM_STATUS_ACTIVE
	item.SellBalance = item.SellAmount
	if offer.Status == bean.OFFER_STORE_STATUS_CREATED {
		offer.Status = bean.OFFER_STORE_STATUS_ACTIVE
	}
	if item.Status == bean.OFFER_STORE_ITEM_STATUS_ACTIVE &&
		item.SubStatus == bean.OFFER_STORE_ITEM_STATUS_REFILLING {
		item.SubStatus = bean.OFFER_STORE_ITEM_STATUS_REFILLED
	}
	offer.ItemSnapshots[item.Currency] = item
	err := s.dao.UpdateOfferStoreItemActive(offer, item)
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	notification.SendOfferStoreNotification(offer, item)
	if item.SubStatus == bean.OFFER_STORE_ITEM_STATUS_REFILLED {
		dao.OfferStoreDaoInst.UpdateNotificationOfferStoreItem(offer, item)
	}

	return
}

func (s OfferStoreService) UpdateOnChainRefillBalanceOfferStore(offerId string, currency string) (offer bean.OfferStore, ce SimpleContextError) {
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	item := *GetOfferStoreItem(*s.dao, offerId, currency, &ce)
	if ce.HasError() {
		return
	}
	if item.SubStatus != bean.OFFER_STORE_ITEM_STATUS_REFILLING {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	item.SubStatus = bean.OFFER_STORE_ITEM_STATUS_REFILLED
	offer.ItemSnapshots[item.Currency] = item

	oldSellAmount := common.StringToDecimal(item.SellBackupAmounts["sell_amount"].(string))
	newSellAmount := common.StringToDecimal(item.SellAmount)
	increasedSellAmount := newSellAmount.Sub(oldSellAmount).String()

	// Only update sell
	err := s.dao.RefillBalanceOfferStoreItem(offer, &item, bean.OfferStoreItem{
		SellAmount: increasedSellAmount,
		BuyAmount:  common.Zero.String(),
	}, bean.OFFER_TYPE_SELL)
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}
	// Only sync to solr and notification firebase
	solr_service.UpdateObject(bean.NewSolrFromOfferStore(offer, item))
	dao.OfferStoreDaoInst.UpdateNotificationOfferStoreItem(offer, item)

	return
}

func (s OfferStoreService) UpdateOnChainCloseOfferStore(offerId string) (offer bean.OfferStore, ce SimpleContextError) {
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	profile := *GetProfile(s.userDao, offer.UID, &ce)
	if ce.HasError() {
		return
	}

	itemTO := s.dao.GetOfferStoreItem(offerId, bean.ETH.Code)
	if !itemTO.Found {
		return
	}
	item := itemTO.Object.(bean.OfferStoreItem)
	if item.Status != bean.OFFER_STORE_ITEM_STATUS_CLOSING {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	item.Status = bean.OFFER_STORE_ITEM_STATUS_CLOSED
	if offer.Status == bean.OFFER_STORE_STATUS_CLOSING {
		offer.Status = bean.OFFER_STORE_STATUS_CLOSED
	}

	profile.ActiveOfferStores[item.Currency] = false
	offer.ItemSnapshots[item.Currency] = item
	err := s.dao.UpdateOfferStoreItemClosed(offer, item, profile)
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	notification.SendOfferStoreNotification(offer, item)

	return
}

func (s OfferStoreService) UpdateOnChainOfferStoreShake(offerId string, offerShakeId string, hid int64, oldStatus string, newStatus string) (offerShake bean.OfferStoreShake, ce SimpleContextError) {
	offer := *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}

	offerShake = *GetOfferStoreShake(*s.dao, offerId, offerShakeId, &ce)
	if ce.HasError() {
		return
	}
	if offerShake.Status != oldStatus {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	// Good
	offerShake.Status = newStatus
	itemTO := s.dao.GetOfferStoreItem(offerId, offerShake.Currency)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, itemTO) {
		return
	}
	item := itemTO.Object.(bean.OfferStoreItem)
	if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_SHAKE || offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_REJECTED {
		var err error
		if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_SHAKE {
			// SHAKE
			err = s.dao.UpdateOfferStoreShakeBalance(offer, &item, offerShake, true)
			if err == nil {
				s.updatePendingTransCount(offer, offerShake, offer.UID)
			}
		} else if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_REJECTED {
			// REJECTED
			err = s.dao.UpdateOfferStoreShakeBalance(offer, &item, offerShake, false)
			s.updateFailedTransCount(offer, offerShake, offerId)
		}
		offer.ItemSnapshots[item.Currency] = item
		if err != nil {
			ce.SetError(api_error.UpdateDataFailed, err)
		}
	} else if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_COMPLETED {
		s.updateSuccessTransCount(offer, offerShake, offerId)
	}
	if offerShake.Hid == 0 {
		offerShake.Hid = hid
	}

	err := s.dao.UpdateOfferStoreShake(offerId, offerShake, offerShake.GetChangeStatus())
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}

	notification.SendOfferStoreShakeNotification(offerShake, offer)
	notification.SendOfferStoreNotification(offer, item)

	return
}

func (s OfferStoreService) ActiveOnChainOfferStore(offerId string, hid int64) (bean.OfferStore, SimpleContextError) {
	return s.UpdateOnChainInitOfferStore(offerId, hid, bean.ETH.Code)
}

func (s OfferStoreService) CloseOnChainOfferStore(offerId string) (bean.OfferStore, SimpleContextError) {
	return s.UpdateOnChainCloseOfferStore(offerId)
}

func (s OfferStoreService) RefillBalanceOnChainOfferStore(offerId string) (bean.OfferStore, SimpleContextError) {
	return s.UpdateOnChainRefillBalanceOfferStore(offerId, bean.ETH.Code)
}

func (s OfferStoreService) PreShakeOnChainOfferStoreShake(offerId string, offerShakeId string, hid int64) (bean.OfferStoreShake, SimpleContextError) {
	return s.UpdateOnChainOfferStoreShake(offerId, offerShakeId, hid, bean.OFFER_STORE_SHAKE_STATUS_PRE_SHAKING, bean.OFFER_STORE_SHAKE_STATUS_PRE_SHAKE)
}

func (s OfferStoreService) CancelOnChainOfferStoreShake(offerId string, offerShakeId string) (bean.OfferStoreShake, SimpleContextError) {
	return s.UpdateOnChainOfferStoreShake(offerId, offerShakeId, 0, bean.OFFER_STORE_SHAKE_STATUS_CANCELLING, bean.OFFER_STORE_SHAKE_STATUS_CANCELLED)
}

func (s OfferStoreService) ShakeOnChainOfferStoreShake(offerId string, offerShakeId string) (bean.OfferStoreShake, SimpleContextError) {
	return s.UpdateOnChainOfferStoreShake(offerId, offerShakeId, 0, bean.OFFER_STORE_SHAKE_STATUS_SHAKING, bean.OFFER_STORE_SHAKE_STATUS_SHAKE)
}

func (s OfferStoreService) RejectOnChainOfferStoreShake(offerId string, offerShakeId string) (bean.OfferStoreShake, SimpleContextError) {
	return s.UpdateOnChainOfferStoreShake(offerId, offerShakeId, 0, bean.OFFER_STORE_SHAKE_STATUS_REJECTING, bean.OFFER_STORE_SHAKE_STATUS_REJECTED)
}

func (s OfferStoreService) CompleteOnChainOfferStoreShake(offerId string, offerShakeId string) (bean.OfferStoreShake, SimpleContextError) {
	return s.UpdateOnChainOfferStoreShake(offerId, offerShakeId, 0, bean.OFFER_STORE_SHAKE_STATUS_COMPLETING, bean.OFFER_STORE_SHAKE_STATUS_COMPLETED)
}

func (s OfferStoreService) ActiveOffChainOfferStore(address string, amountStr string, currency string) (offer bean.OfferStore, ce SimpleContextError) {
	addressMapTO := s.offerDao.GetOfferAddress(address)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, addressMapTO) {
		return
	}
	if ce.NotFound {
		ce.SetStatusKey(api_error.ResourceNotFound)
		return
	}
	addressMap := addressMapTO.Object.(bean.OfferAddressMap)

	itemTO := s.dao.GetOfferStoreItemByPath(addressMap.OfferRef)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, itemTO) {
		return
	}
	item := itemTO.Object.(bean.OfferStoreItem)
	if item.Status != bean.OFFER_STATUS_CREATED {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	inputAmount := common.StringToDecimal(amountStr)
	offerAmount := common.StringToDecimal(item.SellTotalAmount)

	// Check amount need to deposit
	sub := offerAmount.Sub(inputAmount)

	if sub.Equal(common.Zero) {
		// Good
		_, ce = s.UpdateOnChainInitOfferStore(addressMap.Offer, 0, currency)
		if ce.HasError() {
			return
		}
	} else {
		ce.SetStatusKey(api_error.InvalidAmount)
	}

	return
}

func (s OfferStoreService) RefillBalanceOffChainOfferStore(address string, amountStr string, currency string) (offer bean.OfferStore, ce SimpleContextError) {
	addressMapTO := s.offerDao.GetOfferAddress(address)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, addressMapTO) {
		return
	}
	if ce.NotFound {
		ce.SetStatusKey(api_error.ResourceNotFound)
		return
	}
	addressMap := addressMapTO.Object.(bean.OfferAddressMap)

	itemTO := s.dao.GetOfferStoreItemByPath(addressMap.OfferRef)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, itemTO) {
		return
	}
	item := itemTO.Object.(bean.OfferStoreItem)
	if item.SubStatus != bean.OFFER_STORE_ITEM_STATUS_REFILLING {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	inputAmount := common.StringToDecimal(amountStr)

	oldSellAmount := common.StringToDecimal(item.SellBackupAmounts["sell_total_amount"].(string))
	newSellAmount := common.StringToDecimal(item.SellTotalAmount)
	offerAmount := newSellAmount.Sub(oldSellAmount)

	// Check amount need to deposit
	sub := offerAmount.Sub(inputAmount)

	if sub.Equal(common.Zero) {
		// Good
		_, ce = s.UpdateOnChainRefillBalanceOfferStore(addressMap.Offer, currency)
		if ce.HasError() {
			return
		}
	} else {
		ce.SetStatusKey(api_error.InvalidAmount)
	}

	return
}

func (s OfferStoreService) PreShakeOffChainOfferStoreShake(address string, amountStr string) (offer bean.OfferStoreShake, ce SimpleContextError) {
	addressMapTO := s.offerDao.GetOfferAddress(address)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, addressMapTO) {
		return
	}
	if ce.NotFound {
		ce.SetStatusKey(api_error.ResourceNotFound)
		return
	}
	addressMap := addressMapTO.Object.(bean.OfferAddressMap)

	offerShakeTO := s.dao.GetOfferStoreShakeByPath(addressMap.OfferRef)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, offerShakeTO) {
		return
	}
	offer = offerShakeTO.Object.(bean.OfferStoreShake)
	if offer.Status != bean.OFFER_STATUS_SHAKING {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	inputAmount := common.StringToDecimal(amountStr)
	offerAmount := common.StringToDecimal(offer.Amount)

	// Check amount need to deposit
	sub := decimal.NewFromFloat(1)
	// Check amount need to deposit
	sub = offerAmount.Sub(inputAmount)

	if sub.Equal(common.Zero) {
		// Good
		ids := strings.Split(offer.OffChainId, "-")
		_, ce = s.UpdateOnChainOfferStoreShake(ids[0], addressMap.Offer, 0, bean.OFFER_STORE_SHAKE_STATUS_SHAKING, bean.OFFER_STORE_SHAKE_STATUS_SHAKE)
		if ce.HasError() {
			return
		}
	} else {
		// TODO Process to refund?
	}

	return
}

func (s OfferStoreService) FinishOfferStorePendingTransfer(ref string) (offer bean.OfferStore, ce SimpleContextError) {
	return
}

func (s OfferStoreService) FinishOfferStoreShakePendingTransfer(ref string) (offerShake bean.OfferStoreShake, ce SimpleContextError) {
	to := s.dao.GetOfferStoreShakeByPath(ref)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, to) {
		return
	}
	offerShake = to.Object.(bean.OfferStoreShake)
	offer := *GetOfferStore(*s.dao, offerShake.UID, &ce)
	if ce.HasError() {
		return
	}

	if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_COMPLETING {
		offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_COMPLETED
	} else if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_REJECTING {
		offerShake.Status = bean.OFFER_STORE_SHAKE_STATUS_REJECTED
	}

	err := s.dao.UpdateOfferStoreShake(offer.Id, offerShake, offerShake.GetChangeStatus())
	if ce.SetError(api_error.UpdateDataFailed, err) {
		return
	}
	notification.SendOfferStoreShakeNotification(offerShake, offer)

	return
}

func (s OfferStoreService) ReviewOfferStore(userId string, offerId string, score int64, offerShakeId string) (offer bean.OfferStore, ce SimpleContextError) {
	if score < 0 && score > 5 {
		ce.SetStatusKey(api_error.InvalidQueryParam)
	}

	GetProfile(s.userDao, userId, &ce)
	if ce.HasError() {
		return
	}
	offer = *GetOfferStore(*s.dao, offerId, &ce)
	if ce.HasError() {
		return
	}
	offerShake := *GetOfferStoreShake(*s.dao, offerId, offerShakeId, &ce)
	if ce.HasError() {
		return
	}
	if offerShake.UID != userId {
		ce.SetStatusKey(api_error.InvalidUserToCompleteHandshake)
		return
	}
	if offerShake.Status != bean.OFFER_STORE_SHAKE_STATUS_COMPLETING && offerShake.Status != bean.OFFER_STORE_SHAKE_STATUS_COMPLETED {
		ce.SetStatusKey(api_error.OfferStatusInvalid)
		return
	}

	reviewTO := s.dao.GetOfferStoreReview(offer.Id, offerShakeId)
	if reviewTO.Found {
		ce.SetStatusKey(api_error.OfferStoreExists)
		return
	}

	offer.ReviewCount += 1
	offer.Review += score

	err := s.dao.AddOfferStoreReview(offer, bean.OfferStoreReview{
		Id:    offerShakeId,
		UID:   userId,
		Score: score,
	})
	if err != nil {
		ce.SetError(api_error.UpdateDataFailed, err)
		return
	}

	notification.SendOfferStoreNotification(offer, bean.OfferStoreItem{})

	return
}

func (s OfferStoreService) UpdateOfferStoreShakeLocation(userId string, offerId string, offerShakeId string, body bean.OfferStoreShakeLocation) (offerLocation bean.OfferStoreShakeLocation, ce SimpleContextError) {
	data := body.Data
	offerShake := *GetOfferStoreShake(*s.dao, offerId, offerShakeId, &ce)
	if ce.HasError() {
		return
	}

	locationType := "GPS"
	if data[0:1] != "G" {
		locationType = "IP"
	}

	lat1n, _ := strconv.Atoi(data[1:2])
	lat1 := data[2 : 2+lat1n]
	lat2n, _ := strconv.Atoi(data[2+lat1n : 2+lat1n+1])
	lat2 := data[2+lat1n+1 : 2+lat1n+1+lat2n]
	startLong := 2 + lat1n + 1 + lat2n
	long1n, _ := strconv.Atoi(data[startLong : startLong+1])
	long1 := data[startLong+1 : startLong+1+long1n]
	long2n, _ := strconv.Atoi(data[startLong+1+long1n : startLong+1+long1n+1])
	long2 := data[startLong+1+long1n+1 : startLong+1+long1n+1+long2n]

	lat, _ := decimal.NewFromString(fmt.Sprintf("%s.%s", lat1, lat2))
	long, _ := decimal.NewFromString(fmt.Sprintf("%s.%s", long1, long2))

	offerLocation = body
	offerLocation.ActionUID = userId
	offerLocation.Offer = offerId
	offerLocation.OfferShake = offerShakeId
	offerLocation.LocationType = locationType
	offerLocation.Latitude, _ = lat.Float64()
	offerLocation.Longitude, _ = long.Float64()

	s.dao.UpdateOfferStoreShakeLocation(userId, offerShake, offerLocation)

	return
}

func (s OfferStoreService) GetQuote(quoteType string, amountStr string, currency string, fiatCurrency string) (price decimal.Decimal, fiatPrice decimal.Decimal,
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
		price := common.StringToDecimal(resp.Amount)
		fiatPrice = price.Mul(rateNumber)
		fiatAmount = tmpAmount.Mul(price)
	} else if quoteType == "sell" {
		resp, errResp := coinbase_service.GetSellPrice(currency)
		err = errResp
		if err != nil {
			return
		}
		price := common.StringToDecimal(resp.Amount)
		fiatPrice = price.Mul(rateNumber)
		fiatAmount = tmpAmount.Mul(price)
	} else {
		err = errors.New(api_error.InvalidQueryParam)
	}

	return
}

func (s OfferStoreService) GetCurrentFreeStart(userId string, token string) (freeStart bean.OfferStoreFreeStart, ce SimpleContextError) {
	systemConfigTO := s.miscDao.GetSystemConfigFromCache(bean.CONFIG_OFFER_STORE_FREE_START)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, systemConfigTO) {
		return
	}
	systemConfig := systemConfigTO.Object.(bean.SystemConfig)
	// There is no free start on
	if systemConfig.Value == bean.OFFER_STORE_FREE_START_OFF {
		return
	}
	to := s.dao.GetOfferStoreFreeStartUser(userId)
	if to.Error != nil {
		ce.FeedDaoTransfer(api_error.GetDataFailed, to)
		return
	}
	if to.Found {
		freeStartTest := to.Object.(bean.OfferStoreFreeStartUser)
		if freeStartTest.Status == bean.OFFER_STORE_FREE_START_STATUS_DONE {
			return
		}
	}

	freeStarts, err := s.dao.ListOfferStoreFreeStart(token)
	if err != nil {
		ce.SetError(api_error.GetDataFailed, err)
	}

	for _, item := range freeStarts {
		if item.Count < item.Limit {
			freeStart = item
			break
		}
	}

	return
}

func (s OfferStoreService) SyncOfferStoreToSolr(offerId string) (offer bean.OfferStore, ce SimpleContextError) {
	offerTO := s.dao.GetOfferStore(offerId)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, offerTO) {
		return
	}
	offer = offerTO.Object.(bean.OfferStore)
	solr_service.UpdateObject(bean.NewSolrFromOfferStore(offer, bean.OfferStoreItem{}))

	return
}

func (s OfferStoreService) SyncOfferStoreShakeToSolr(offerId, offerShakeId string) (offerShake bean.OfferStoreShake, ce SimpleContextError) {
	offerStoreTO := s.dao.GetOfferStore(offerId)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, offerStoreTO) {
		return
	}
	offer := offerStoreTO.Object.(bean.OfferStore)
	offerShakeTO := s.dao.GetOfferStoreShake(offerId, offerShakeId)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, offerShakeTO) {
		return
	}
	offerShake = offerShakeTO.Object.(bean.OfferStoreShake)
	solr_service.UpdateObject(bean.NewSolrFromOfferStoreShake(offerShake, offer))

	return
}

func (s OfferStoreService) prepareRefillOfferStoreItem(offer *bean.OfferStore, item *bean.OfferStoreItem, body *bean.OfferStoreItem, ce *SimpleContextError) {
	s.checkOfferStoreItemAmount(item, ce)
	if ce.HasError() {
		return
	}

	s.generateSystemAddress(*offer, item, ce)

	// Copy to back up
	item.SellBackupAmounts = map[string]interface{}{
		"sell_amount":       fmt.Sprintf("%s", item.SellAmount),
		"sell_total_amount": fmt.Sprintf("%s", item.SellTotalAmount),
	}

	sellTotalAmount := common.StringToDecimal(item.SellTotalAmount)

	sellAmount := common.StringToDecimal(item.SellAmount)
	bodySellAmount := common.StringToDecimal(body.SellAmount)
	sellAmount = sellAmount.Add(bodySellAmount)
	item.SellAmount = sellAmount.String()

	if bodySellAmount.GreaterThan(common.Zero) {
		exchFeeTO := s.miscDao.GetSystemFeeFromCache(bean.FEE_KEY_EXCHANGE)
		if ce.FeedDaoTransfer(api_error.GetDataFailed, exchFeeTO) {
			return
		}
		exchFeeObj := exchFeeTO.Object.(bean.SystemFee)
		exchFee := decimal.NewFromFloat(exchFeeObj.Value).Round(6)
		fee := bodySellAmount.Mul(exchFee)

		item.SellTotalAmount = sellTotalAmount.Add(bodySellAmount).Add(fee).String()
		body.SellTotalAmount = bodySellAmount.Add(fee).String()

		item.SubStatus = bean.OFFER_STORE_ITEM_STATUS_REFILLING
		body.SubStatus = bean.OFFER_STORE_ITEM_STATUS_REFILLING
	}

	buyAmount := common.StringToDecimal(item.BuyAmount)
	bodyBuyAmount := common.StringToDecimal(body.BuyAmount)
	buyAmount = buyAmount.Add(bodyBuyAmount)
	item.BuyAmount = buyAmount.String()

	offer.ItemSnapshots[item.Currency] = *item

	return
}

func (s OfferStoreService) prepareOfferStore(offer *bean.OfferStore, item *bean.OfferStoreItem, profile *bean.Profile, ce *SimpleContextError) {
	currencyInst := bean.CurrencyMapping[item.Currency]
	if currencyInst.Code == "" {
		ce.SetStatusKey(api_error.UnsupportedCurrency)
		return
	}

	if profile.ActiveOfferStores == nil {
		profile.ActiveOfferStores = make(map[string]bool)
	}
	if check, ok := profile.ActiveOfferStores[currencyInst.Code]; ok && check {
		// Has Key and already had setup
		ce.SetStatusKey(api_error.TooManyOffer)
		return
	}

	profile.ActiveOfferStores[currencyInst.Code] = true
	offer.ItemFlags = profile.ActiveOfferStores
	offer.UID = profile.UserId

	s.checkOfferStoreItemAmount(item, ce)
	if ce.HasError() {
		return
	}

	s.generateSystemAddress(*offer, item, ce)

	sellAmount := common.StringToDecimal(item.SellAmount)
	if sellAmount.Equal(common.Zero) {
		// Only the case that shop doesn't sell, so don't need to wait to active
		item.Status = bean.OFFER_STORE_ITEM_STATUS_ACTIVE
		// So active the store as well
		offer.Status = bean.OFFER_STORE_STATUS_ACTIVE
	} else {
		item.Status = bean.OFFER_STORE_ITEM_STATUS_CREATED
	}
	if offer.Status != bean.OFFER_STORE_STATUS_ACTIVE {
		offer.Status = bean.OFFER_STORE_STATUS_CREATED
	}

	minAmount := bean.MIN_ETH
	if item.Currency == bean.BTC.Code {
		minAmount = bean.MIN_BTC
	}
	if item.Currency == bean.BCH.Code {
		minAmount = bean.MIN_BCH
	}
	item.BuyBalance = item.BuyAmount
	item.BuyAmountMin = minAmount.String()
	item.SellBalance = "0"
	item.SellAmountMin = minAmount.String()
	item.SellTotalAmount = "0"
	item.CreatedAt = time.Now().UTC()
	amount := common.StringToDecimal(item.SellAmount)
	if amount.GreaterThan(common.Zero) {
		exchFeeTO := s.miscDao.GetSystemFeeFromCache(bean.FEE_KEY_EXCHANGE)
		if ce.FeedDaoTransfer(api_error.GetDataFailed, exchFeeTO) {
			return
		}
		exchFeeObj := exchFeeTO.Object.(bean.SystemFee)
		exchFee := decimal.NewFromFloat(exchFeeObj.Value).Round(6)
		fee := amount.Mul(exchFee)
		item.SellTotalAmount = amount.Add(fee).String()
	}

	if offer.ItemSnapshots == nil {
		offer.ItemSnapshots = make(map[string]bean.OfferStoreItem)
	}
	offer.ItemSnapshots[item.Currency] = *item
}

func (s OfferStoreService) checkOfferStoreItemAmount(item *bean.OfferStoreItem, ce *SimpleContextError) {
	// Minimum amount
	sellAmount, errFmt := decimal.NewFromString(item.SellAmount)
	if ce.SetError(api_error.InvalidRequestBody, errFmt) {
		return
	}
	if item.Currency == bean.ETH.Code {
		if sellAmount.GreaterThan(common.Zero) && sellAmount.LessThan(bean.MIN_ETH) {
			ce.SetStatusKey(api_error.AmountIsTooSmall)
			return
		}
	}
	if item.Currency == bean.BTC.Code {
		if sellAmount.GreaterThan(common.Zero) && sellAmount.LessThan(bean.MIN_BTC) {
			ce.SetStatusKey(api_error.AmountIsTooSmall)
			return
		}
	}
	if item.Currency == bean.BCH.Code {
		if sellAmount.GreaterThan(common.Zero) && sellAmount.LessThan(bean.MIN_BCH) {
			ce.SetStatusKey(api_error.AmountIsTooSmall)
			return
		}
	}
	if item.SellPercentage != "" {
		// Convert to 0.0x
		percentage, errFmt := decimal.NewFromString(item.SellPercentage)
		if ce.SetError(api_error.InvalidRequestBody, errFmt) {
			return
		}
		item.SellPercentage = percentage.Div(decimal.NewFromFloat(100)).String()
	} else {
		item.SellPercentage = "0"
	}

	buyAmount, errFmt := decimal.NewFromString(item.BuyAmount)
	if ce.SetError(api_error.InvalidRequestBody, errFmt) {
		return
	}
	if item.Currency == bean.ETH.Code {
		if buyAmount.GreaterThan(common.Zero) && buyAmount.LessThan(bean.MIN_ETH) {
			ce.SetStatusKey(api_error.AmountIsTooSmall)
			return
		}
	}
	if item.Currency == bean.BTC.Code {
		if buyAmount.GreaterThan(common.Zero) && buyAmount.LessThan(bean.MIN_BTC) {
			ce.SetStatusKey(api_error.AmountIsTooSmall)
			return
		}
	}
	if item.Currency == bean.BCH.Code {
		if buyAmount.GreaterThan(common.Zero) && buyAmount.LessThan(bean.MIN_BCH) {
			ce.SetStatusKey(api_error.AmountIsTooSmall)
			return
		}
	}
	if item.BuyPercentage != "" {
		// Convert to 0.0x
		percentage, errFmt := decimal.NewFromString(item.BuyPercentage)
		if ce.SetError(api_error.InvalidRequestBody, errFmt) {
			return
		}
		item.BuyPercentage = percentage.Div(decimal.NewFromFloat(100)).String()
	} else {
		item.BuyPercentage = "0"
	}
}

func (s OfferStoreService) generateSystemAddress(offer bean.OfferStore, item *bean.OfferStoreItem, ce *SimpleContextError) {
	// Only BTC need to generate address to transfer in
	if item.Currency != bean.ETH.Code {
		systemConfigTO := s.miscDao.GetSystemConfigFromCache(bean.CONFIG_BTC_WALLET)
		if ce.FeedDaoTransfer(api_error.GetDataFailed, systemConfigTO) {
			return
		}
		systemConfig := systemConfigTO.Object.(bean.SystemConfig)
		item.WalletProvider = systemConfig.Value
		if systemConfig.Value == bean.BTC_WALLET_COINBASE {
			addressResponse, err := coinbase_service.GenerateAddress(item.Currency)
			if err != nil {
				ce.SetError(api_error.ExternalApiFailed, err)
				return
			}
			item.SystemAddress = addressResponse.Data.Address

		} else if systemConfig.Value == bean.BTC_WALLET_BLOCKCHAINIO {
			client := blockchainio_service.BlockChainIOClient{}
			address, err := client.GenerateAddress(offer.Id)
			if err != nil {
				ce.SetError(api_error.ExternalApiFailed, err)
				return
			}
			item.SystemAddress = address
		} else {
			ce.SetStatusKey(api_error.InvalidConfig)
		}
	}
}

// TODO remove func duplicate
func (s OfferStoreService) generateSystemAddressForShake(offer bean.OfferStore, offerShake *bean.OfferStoreShake, ce *SimpleContextError) {
	// Only BTC need to generate address to transfer in
	if offerShake.Currency != bean.ETH.Code {
		systemConfigTO := s.miscDao.GetSystemConfigFromCache(bean.CONFIG_BTC_WALLET)
		if ce.FeedDaoTransfer(api_error.GetDataFailed, systemConfigTO) {
			return
		}
		systemConfig := systemConfigTO.Object.(bean.SystemConfig)
		offerShake.WalletProvider = systemConfig.Value
		if systemConfig.Value == bean.BTC_WALLET_COINBASE {
			addressResponse, err := coinbase_service.GenerateAddress(offerShake.Currency)
			if err != nil {
				ce.SetError(api_error.ExternalApiFailed, err)
				return
			}
			offerShake.SystemAddress = addressResponse.Data.Address

		} else if systemConfig.Value == bean.BTC_WALLET_BLOCKCHAINIO {
			client := blockchainio_service.BlockChainIOClient{}
			address, err := client.GenerateAddress(offer.Id)
			if err != nil {
				ce.SetError(api_error.ExternalApiFailed, err)
				return
			}
			offerShake.SystemAddress = address
		} else {
			ce.SetStatusKey(api_error.InvalidConfig)
		}
	}
}

func (s OfferStoreService) transferCrypto(offer *bean.OfferStore, offerShake *bean.OfferStoreShake, ce *SimpleContextError) {
	offerStoreItemTO := s.dao.GetOfferStoreItem(offer.UID, offerShake.Currency)
	if offerStoreItemTO.HasError() {
		ce.SetStatusKey(api_error.GetDataFailed)
		return
	}
	offerStoreItem := offerStoreItemTO.Object.(bean.OfferStoreItem)
	userAddress := offerShake.UserAddress
	actionUID := offerShake.UID

	if offerShake.Type == bean.OFFER_TYPE_BUY {
		userAddress = offerStoreItem.UserAddress
		actionUID = offer.UID
	}

	if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_COMPLETED {
		if userAddress != "" {
			//Transfer
			description := fmt.Sprintf("Transfer to userId %s offerShakeId %s status %s", actionUID, offerShake.Id, offerShake.Status)

			var response1 interface{}
			// var response2 interface{}
			var userId string
			transferAmount := offerShake.Amount
			if offerShake.Type == bean.OFFER_TYPE_BUY {
				response1 = s.sendTransaction(offerStoreItem.UserAddress, offerShake.TotalAmount, offerShake.Currency, description, offerShake.Id, offerStoreItem.WalletProvider, ce)
				userId = offer.UID
				transferAmount = offerShake.TotalAmount
			} else {
				response1 = s.sendTransaction(offerShake.UserAddress, offerShake.Amount, offerShake.Currency, description, offerShake.Id, offerStoreItem.WalletProvider, ce)
				userId = offerShake.UID
			}
			s.miscDao.AddCryptoTransferLog(bean.CryptoTransferLog{
				Provider:         offerStoreItem.WalletProvider,
				ProviderResponse: response1,
				DataType:         bean.OFFER_ADDRESS_MAP_OFFER_STORE_SHAKE,
				DataRef:          dao.GetOfferStoreShakeItemPath(offer.Id, offerShake.Id),
				UID:              userId,
				Description:      description,
				Amount:           transferAmount,
				Currency:         offerShake.Currency,
			})

			// Transfer reward
			//if offerStoreItem.RewardAddress != "" {
			//	rewardDescription := fmt.Sprintf("Transfer reward to userId %s offerId %s", offerStore.UID, offerStoreShake.Id)
			//	response2 = s.sendTransaction(offerStoreItem.RewardAddress, offerStoreShake.Reward, offerStoreItem.Currency, rewardDescription,
			//		fmt.Sprintf("%s_reward", offerStoreShake.Id), offerStoreItem.WalletProvider, ce)
			//
			//	s.miscDao.AddCryptoTransferLog(bean.CryptoTransferLog{
			//		Provider:         offerStoreItem.WalletProvider,
			//		ProviderResponse: response2,
			//		DataType:         bean.OFFER_ADDRESS_MAP_OFFER_STORE_SHAKE,
			//		DataRef:          dao.GetOfferStoreShakeItemPath(offerStore.Id, offerStoreShake.Id),
			//		UID:              offerStore.UID,
			//		Description:      description,
			//		Amount:           offerStoreShake.Amount,
			//		Currency:         offerStoreShake.Currency,
			//	})
			//}
			// Just logging the error, don't throw it
			//if ce.HasError() {
			//	return
			//}
		} else {
			ce.SetStatusKey(api_error.InvalidRequestBody)
			return
		}
	}
}

func (s OfferStoreService) sendTransaction(address string, amountStr string, currency string, description string, withdrawId string,
	walletProvider string, ce *SimpleContextError) interface{} {
	// Only BTC
	if currency != bean.ETH.Code {

		if walletProvider == bean.BTC_WALLET_COINBASE {
			response, err := coinbase_service.SendTransaction(address, amountStr, currency, description, withdrawId)
			fmt.Println(response)
			fmt.Println(err)
			if ce.SetError(api_error.ExternalApiFailed, err) {
				return ""
			}
			return response
		} else if walletProvider == bean.BTC_WALLET_BLOCKCHAINIO {
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

func (s OfferStoreService) updateSuccessTransCount(offer bean.OfferStore, offerShake bean.OfferStoreShake, actionUID string) (transCount1 bean.TransactionCount, transCount2 bean.TransactionCount) {
	transCountTO := s.transDao.GetTransactionCount(offer.UID, offerShake.Currency)
	if transCountTO.HasError() {
		return
	}
	transCount1 = transCountTO.Object.(bean.TransactionCount)
	transCount1.Currency = offerShake.Currency
	transCount1.Success += 1
	transCount1.Pending -= 1
	if transCount1.Pending < 0 {
		// Just for prevent weird number
		transCount1.Pending = 0
	}
	if offerShake.IsTypeSell() {
		sellAmount := common.StringToDecimal(transCount1.SellAmount)
		amount := common.StringToDecimal(offerShake.Amount)
		transCount1.SellAmount = sellAmount.Add(amount).String()

		if fiatAmountObj, ok := transCount1.SellFiatAmounts[offerShake.FiatCurrency]; ok {
			fiatAmount := common.StringToDecimal(fiatAmountObj.Amount)
			newFiatAmount := common.StringToDecimal(offerShake.FiatAmount)
			fiatAmountObj.Amount = fiatAmount.Add(newFiatAmount).String()
		} else {
			transCount1.SellFiatAmounts[offerShake.FiatCurrency] = bean.TransactionFiatAmount{
				Currency: offerShake.FiatCurrency,
				Amount:   offerShake.FiatAmount,
			}
		}
	} else {
		buyAmount := common.StringToDecimal(transCount1.BuyAmount)
		amount := common.StringToDecimal(offerShake.Amount)
		transCount1.BuyAmount = buyAmount.Add(amount).String()

		if fiatAmountObj, ok := transCount1.BuyFiatAmounts[offerShake.FiatCurrency]; ok {
			fiatAmount := common.StringToDecimal(fiatAmountObj.Amount)
			newFiatAmount := common.StringToDecimal(offerShake.FiatAmount)
			fiatAmountObj.Amount = fiatAmount.Add(newFiatAmount).String()
		} else {
			transCount1.BuyFiatAmounts[offerShake.FiatCurrency] = bean.TransactionFiatAmount{
				Currency: offerShake.FiatCurrency,
				Amount:   offerShake.FiatAmount,
			}
		}
	}

	//transCountTO = s.transDao.GetTransactionCount(offerShake.UID, offerShake.Currency)
	//if transCountTO.HasError() {
	//	return
	//}
	//transCount2 = transCountTO.Object.(bean.TransactionCount)
	//transCount2.Currency = offerShake.Currency
	//transCount2.Success += 1

	s.transDao.UpdateTransactionCount(offer.UID, offerShake.Currency, transCount1.GetUpdateSuccess())
	//s.transDao.UpdateTransactionCount(offerShake.UID, offerShake.Currency, transCount2.GetUpdateSuccess())

	return
}

func (s OfferStoreService) updatePendingTransCount(offer bean.OfferStore, offerShake bean.OfferStoreShake, actionUID string) (transCount bean.TransactionCount) {
	transCountTO := s.transDao.GetTransactionCount(offer.UID, offerShake.Currency)
	if transCountTO.HasError() {
		return
	}
	transCount = transCountTO.Object.(bean.TransactionCount)
	transCount.Currency = offerShake.Currency
	transCount.Pending += 1

	s.transDao.UpdateTransactionCount(offer.UID, offerShake.Currency, transCount.GetUpdatePending())

	return
}

func (s OfferStoreService) updateFailedTransCount(offer bean.OfferStore, offerShake bean.OfferStoreShake, actionUID string) (transCount bean.TransactionCount) {
	transCountTO := s.transDao.GetTransactionCount(offer.UID, offerShake.Currency)
	if transCountTO.HasError() {
		return
	}
	transCount = transCountTO.Object.(bean.TransactionCount)
	transCount.Currency = offerShake.Currency
	if actionUID == offer.UID {
		transCount.Failed += 1
	}
	transCount.Pending -= 1
	if transCount.Pending < 0 {
		// Just for prevent weird number
		transCount.Pending = 0
	}
	s.transDao.UpdateTransactionCount(offer.UID, offerShake.Currency, transCount.GetUpdateFailed())

	return
}

func (s OfferStoreService) setupOfferShakePrice(offer *bean.OfferStoreShake, ce *SimpleContextError) {
	userOfferType := bean.OFFER_TYPE_SELL
	if offer.IsTypeSell() {
		userOfferType = bean.OFFER_TYPE_BUY
	}
	_, fiatPrice, fiatAmount, err := s.GetQuote(userOfferType, offer.Amount, offer.Currency, offer.FiatCurrency)
	if ce.SetError(api_error.GetDataFailed, err) {
		return
	}

	offer.Price = fiatPrice.Round(2).String()
	offer.FiatAmount = fiatAmount.Round(2).String()
}

func (s OfferStoreService) setupOfferShakeAmount(offerShake *bean.OfferStoreShake, ce *SimpleContextError) {
	exchFeeTO := s.miscDao.GetSystemFeeFromCache(bean.FEE_KEY_EXCHANGE)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, exchFeeTO) {
		return
	}
	exchFeeObj := exchFeeTO.Object.(bean.SystemFee)
	exchCommTO := s.miscDao.GetSystemFeeFromCache(bean.FEE_KEY_EXCHANGE_COMMISSION)
	if ce.FeedDaoTransfer(api_error.GetDataFailed, exchCommTO) {
		return
	}
	exchCommObj := exchCommTO.Object.(bean.SystemFee)

	exchFee := decimal.NewFromFloat(exchFeeObj.Value).Round(6)
	exchComm := decimal.NewFromFloat(exchCommObj.Value).Round(6)
	amount := common.StringToDecimal(offerShake.Amount)
	fee := amount.Mul(exchFee)
	// reward := amount.Mul(exchComm)
	// For now
	reward := decimal.NewFromFloat(0)

	offerShake.FeePercentage = exchFee.String()
	offerShake.RewardPercentage = exchComm.String()
	offerShake.Fee = fee.String()
	offerShake.Reward = reward.String()
	if offerShake.Type == bean.OFFER_TYPE_SELL {
		offerShake.TotalAmount = amount.Add(fee.Add(reward)).String()
	} else if offerShake.Type == bean.OFFER_TYPE_BUY {
		offerShake.TotalAmount = amount.Sub(fee.Add(reward)).String()
	}
}

func (s OfferStoreService) getOfferProfile(offer bean.OfferStore, offerShake bean.OfferStoreShake, profile bean.Profile, ce *SimpleContextError) (offerProfile bean.Profile) {
	if profile.UserId == offer.UID {
		offerProfile = profile
	} else {
		offerProfileTO := s.userDao.GetProfile(offerShake.UID)
		if ce.FeedDaoTransfer(api_error.GetDataFailed, offerProfileTO) {
			return
		}
		offerProfile = offerProfileTO.Object.(bean.Profile)
	}

	return
}

func (s OfferStoreService) getUsageBalance(offerId string, offerType string, currency string) (decimal.Decimal, error) {
	offerShakes, err := s.dao.ListOfferStoreShake(offerId)
	usage := common.Zero
	if err == nil {
		for _, offerShake := range offerShakes {
			if offerShake.Status != bean.OFFER_STORE_SHAKE_STATUS_REJECTED &&
				offerShake.Status != bean.OFFER_STORE_SHAKE_STATUS_CANCELLED &&
				offerShake.Status != bean.OFFER_STORE_SHAKE_STATUS_COMPLETED &&
				offerShake.Type == offerType && offerShake.Currency == currency {
				amount := common.StringToDecimal(offerShake.Amount)
				usage = usage.Add(amount)
			}
		}
	}
	return usage, err
}

func (s OfferStoreService) countActiveShake(offerId string, offerType string, currency string) (int, error) {
	offerShakes, err := s.dao.ListOfferStoreShake(offerId)
	count := 0
	countInactive := 0
	if err == nil {
		for _, offerShake := range offerShakes {
			if offerType != "" && offerShake.Type == offerType && offerShake.Currency == currency {
				if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_COMPLETED || offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_REJECTED || offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_CANCELLED {
					countInactive += 1
				}
				count += 1
			} else if offerShake.Currency == currency {
				if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_COMPLETED || offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_REJECTED || offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_CANCELLED {
					countInactive += 1
				}
				count += 1
			}
		}
	}
	return count - countInactive, err
}

func (s OfferStoreService) registerFreeStart(userId string, offerItem *bean.OfferStoreItem, ce *SimpleContextError) (freeStartUser bean.OfferStoreFreeStartUser) {
	freeStart, freeStartCE := s.GetCurrentFreeStart(userId, offerItem.FreeStart)
	if ce.FeedContextError(api_error.GetDataFailed, freeStartCE) {
		return
	}
	if freeStart.Reward != "" {
		if freeStart.Reward != offerItem.SellAmount {
			ce.SetStatusKey(api_error.InvalidFreeStartAmount)
		}
		offerItem.FreeStartRef = dao.GetOfferStoreFreeStartItemPath(freeStart.Id)

		freeStartUser.UID = userId
		freeStartUser.Reward = freeStart.Reward
		freeStartUser.Currency = freeStart.Currency
		freeStartUser.FreeStart = freeStart.Id
		err := s.dao.AddOfferStoreFreeStartUser(&freeStart, &freeStartUser)

		if err != nil {
			ce.SetError(api_error.RegisterFreeStartFailed, err)
			return
		}
		// Change address to our address
		offerItem.UserAddress = ethereum_service.GetAddress()
	} else {
		// Cannot find free start for this store
		offerItem.FreeStart = ""
		ce.SetStatusKey(api_error.InvalidFreeStartAmount)
	}
	return
}

func (s OfferStoreService) ScriptUpdateTransactionCount() error {
	t := s.dao.ListOfferStore()
	if !t.HasError() {
		for _, item := range t.Objects {
			offer := item.(bean.OfferStore)
			fmt.Printf("Updating store %s", offer.UID)
			fmt.Println("")

			shakes, err := s.dao.ListOfferStoreShake(offer.UID)
			if err == nil {
				btcTxCount := bean.TransactionCount{
					Currency:        bean.BTC.Code,
					Success:         0,
					Failed:          0,
					Pending:         0,
					BuyAmount:       common.Zero.String(),
					SellAmount:      common.Zero.String(),
					BuyFiatAmounts:  map[string]bean.TransactionFiatAmount{},
					SellFiatAmounts: map[string]bean.TransactionFiatAmount{},
				}
				ethTxCount := bean.TransactionCount{
					Currency:        bean.ETH.Code,
					Success:         0,
					Failed:          0,
					Pending:         0,
					BuyAmount:       common.Zero.String(),
					SellAmount:      common.Zero.String(),
					BuyFiatAmounts:  map[string]bean.TransactionFiatAmount{},
					SellFiatAmounts: map[string]bean.TransactionFiatAmount{},
				}

				txCountMap := map[string]*bean.TransactionCount{
					bean.ETH.Code: &ethTxCount,
					bean.BTC.Code: &btcTxCount,
				}

				for _, offerShake := range shakes {
					currency := offerShake.Currency
					fmt.Printf("Processing shake %s %s", offerShake.Id, offerShake.Currency)
					fmt.Println("")

					if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_COMPLETED {
						txCountMap[currency].Success += 1

						if offerShake.IsTypeSell() {
							sellAmount := common.StringToDecimal(txCountMap[currency].SellAmount)
							amount := common.StringToDecimal(offerShake.Amount)
							txCountMap[currency].SellAmount = sellAmount.Add(amount).String()

							if fiatAmountObj, ok := txCountMap[currency].SellFiatAmounts[offerShake.FiatCurrency]; ok {
								fiatAmount := common.StringToDecimal(fiatAmountObj.Amount)
								newFiatAmount := common.StringToDecimal(offerShake.FiatAmount)
								fiatAmountObj.Amount = fiatAmount.Add(newFiatAmount).String()
							} else {
								txCountMap[currency].SellFiatAmounts[offerShake.FiatCurrency] = bean.TransactionFiatAmount{
									Currency: offerShake.FiatCurrency,
									Amount:   offerShake.FiatAmount,
								}
							}
						} else {
							buyAmount := common.StringToDecimal(txCountMap[currency].BuyAmount)
							amount := common.StringToDecimal(offerShake.Amount)
							txCountMap[currency].BuyAmount = buyAmount.Add(amount).String()

							if fiatAmountObj, ok := txCountMap[currency].BuyFiatAmounts[offerShake.FiatCurrency]; ok {
								fiatAmount := common.StringToDecimal(fiatAmountObj.Amount)
								newFiatAmount := common.StringToDecimal(offerShake.FiatAmount)
								fiatAmountObj.Amount = fiatAmount.Add(newFiatAmount).String()
							} else {
								txCountMap[currency].BuyFiatAmounts[offerShake.FiatCurrency] = bean.TransactionFiatAmount{
									Currency: offerShake.FiatCurrency,
									Amount:   offerShake.FiatAmount,
								}
							}
						}
					} else if offerShake.Status == bean.OFFER_STORE_SHAKE_STATUS_REJECTED {
						txCountMap[currency].Failed += 1
					} else {
						txCountMap[currency].Pending += 1
					}
				}
				if btcTxCount.Pending > 0 || btcTxCount.Success > 0 || btcTxCount.Failed > 0 {
					fmt.Printf("Making update BTC tx count %s", offer.UID)
					fmt.Println("")
					s.transDao.UpdateTransactionCountForce(offer.UID, bean.BTC.Code, btcTxCount.GetUpdateOverride())
				}
				if ethTxCount.Pending > 0 || ethTxCount.Success > 0 || ethTxCount.Failed > 0 {
					fmt.Printf("Making update ETH tx count %s", offer.UID)
					fmt.Println("")
					s.transDao.UpdateTransactionCountForce(offer.UID, bean.ETH.Code, ethTxCount.GetUpdateOverride())
				}
			}
		}
	}

	return t.Error
}

func (s OfferStoreService) ScriptUpdateOfferStoreSolr() error {
	t := s.dao.ListOfferStore()
	if !t.HasError() {
		for _, item := range t.Objects {
			offer := item.(bean.OfferStore)
			fmt.Printf("Updating store %s", offer.UID)
			fmt.Println("")
			solr_service.UpdateObject(bean.NewSolrFromOfferStore(offer, bean.OfferStoreItem{}))
		}
	}
	return t.Error
}
