package dao

import (
	"cloud.google.com/go/firestore"
	"context"
	"fmt"
	"github.com/ninjadotorg/handshake-exchange/bean"
	"github.com/ninjadotorg/handshake-exchange/integration/firebase_service"
	"google.golang.org/api/iterator"
)

type CreditCardDao struct {
}

func (dao CreditCardDao) AddCCTransaction(ccTran bean.CCTransaction) (bean.CCTransaction, error) {
	dbClient := firebase_service.FirestoreClient
	docRef := dbClient.Collection(GetCCTransactionPath(ccTran.UID)).NewDoc()
	ccTran.Id = docRef.ID

	_, err := docRef.Set(context.Background(), ccTran.GetAddCCTransaction())

	return ccTran, err
}

func (dao CreditCardDao) UpdateCCTransaction(ccTran bean.CCTransaction) (bean.CCTransaction, error) {
	dbClient := firebase_service.FirestoreClient
	docRef := dbClient.Doc(GetCCTransactionItemPath(ccTran.UID, ccTran.Id))

	_, err := docRef.Set(context.Background(), ccTran.GetUpdateCCTransaction(), firestore.MergeAll)

	return ccTran, err
}

func (dao CreditCardDao) UpdateCCTransactionStatus(ccTran bean.CCTransaction) (bean.CCTransaction, error) {
	dbClient := firebase_service.FirestoreClient
	docRef := dbClient.Doc(GetCCTransactionItemPath(ccTran.UID, ccTran.Id))

	_, err := docRef.Set(context.Background(), ccTran.GetUpdateStatus(), firestore.MergeAll)

	return ccTran, err
}

func (dao CreditCardDao) ListCCTransactions(userId string, limit int, startAt interface{}) (t TransferObject) {
	ListPagingObjects(GetCCTransactionPath(userId), &t, limit, startAt, func(collRef *firestore.CollectionRef) firestore.Query {
		query := collRef.OrderBy("created_at", firestore.Desc)
		return query
	}, snapshotToCCTransaction)

	return
}

func (dao CreditCardDao) GetCCTransaction(userId string, ccTranId string) TransferObject {
	return dao.GetCCTransactionByPath(GetCCTransactionItemPath(userId, ccTranId))
}

func (dao CreditCardDao) GetCCTransactionByPath(path string) (t TransferObject) {
	// users/{uid}/cc_transactions/{id}
	GetObject(path, &t, snapshotToCCTransaction)
	return
}

func (dao CreditCardDao) AddInstantOffer(offer bean.InstantOffer, transaction bean.Transaction, providerId string) (bean.InstantOffer, error) {
	dbClient := firebase_service.FirestoreClient

	docRef := dbClient.Collection(GetInstantOfferPath(offer.UID)).NewDoc()
	offer.Id = docRef.ID

	pendingOffer := bean.PendingInstantOffer{
		UID:             offer.UID,
		InstantOffer:    offer.Id,
		InstantOfferRef: GetInstantOfferItemPath(offer.UID, offer.Id),
		Duration:        offer.Duration,
		Provider:        offer.Provider,
		ProviderId:      providerId,
		CCMode:          offer.CCMode,
	}
	pendingOfferId := fmt.Sprintf("%s-%s", offer.UID, offer.Id)
	docPendingRef := dbClient.Doc(GetPendingInstantOfferItemPath(pendingOfferId))
	pendingOffer.Id = pendingOfferId

	docTransactionRef := dbClient.Collection(GetTransactionPath(offer.UID)).NewDoc()
	offer.TransactionRef = GetTransactionItemPath(offer.UID, docTransactionRef.ID)

	batch := dbClient.Batch()
	batch.Set(docRef, offer.GetAddInstantOffer())
	batch.Set(docPendingRef, pendingOffer.GetAddInstantOffer())
	batch.Set(docTransactionRef, transaction.GetAddTransaction())
	_, err := batch.Commit(context.Background())

	return offer, err
}

func (dao CreditCardDao) UpdateInstantOffer(offer bean.InstantOffer, transaction bean.Transaction) (bean.InstantOffer, error) {
	dbClient := firebase_service.FirestoreClient
	docRef := dbClient.Doc(GetInstantOfferItemPath(offer.UID, offer.Id))

	pendingOfferId := fmt.Sprintf("%s-%s", offer.UID, offer.Id)
	pendingOfferDocRef := dbClient.Doc(GetPendingInstantOfferItemPath(pendingOfferId))

	docTransactionRef := dbClient.Doc(offer.TransactionRef)

	batch := dbClient.Batch()
	batch.Set(docRef, offer.GetUpdate(), firestore.MergeAll)
	batch.Delete(pendingOfferDocRef)
	batch.Set(docTransactionRef, transaction.GetUpdateStatus(), firestore.MergeAll)
	_, err := batch.Commit(context.Background())

	return offer, err
}

func (dao CreditCardDao) ListInstantOffers(userId string, currency string, limit int, startAt interface{}) (t TransferObject) {
	ListPagingObjects(GetInstantOfferPath(userId), &t, limit, startAt, func(collRef *firestore.CollectionRef) firestore.Query {
		query := collRef.Where("currency", "==", currency).OrderBy("created_at", firestore.Desc)
		return query
	}, snapshotToInstantOffer)

	return
}

func (dao CreditCardDao) GetInstantOffer(userId string, instantOfferId string) TransferObject {
	return dao.GetInstantOfferByPath(GetInstantOfferItemPath(userId, instantOfferId))
}

func (dao CreditCardDao) GetInstantOfferByPath(path string) (t TransferObject) {
	// users/{uid}/instant_offers/{id}
	GetObject(path, &t, snapshotToInstantOffer)
	return
}

func (dao CreditCardDao) ListPendingInstantOffer() ([]bean.PendingInstantOffer, error) {
	dbClient := firebase_service.FirestoreClient

	// pending_instant_offers
	iter := dbClient.Collection(GetPendingInstantOfferPath()).Documents(context.Background())
	offers := make([]bean.PendingInstantOffer, 0)

	for {
		var offer bean.PendingInstantOffer
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return offers, err
		}
		doc.DataTo(&offer)
		offers = append(offers, offer)
	}

	return offers, nil
}

func (dao CreditCardDao) UpdateNotificationInstantOffer(offer bean.InstantOffer) error {
	dbClient := firebase_service.NotificationFirebaseClient

	ref := dbClient.NewRef(GetNotificationInstantOfferItemPath(offer.UID, offer.Id))
	err := ref.Set(context.Background(), offer.GetNotificationUpdate())

	return err
}

func GetCCTransactionPath(userId string) string {
	return fmt.Sprintf("users/%s/cc_transactions", userId)
}

func GetCCTransactionItemPath(userId string, id string) string {
	return fmt.Sprintf("%s/%s", GetCCTransactionPath(userId), id)
}

func GetInstantOfferPath(userId string) string {
	return fmt.Sprintf("users/%s/instant_offers", userId)
}

func GetInstantOfferItemPath(userId string, id string) string {
	return fmt.Sprintf("%s/%s", GetInstantOfferPath(userId), id)
}

func GetPendingInstantOfferPath() string {
	return fmt.Sprintf("pending_instant_offers")
}

func GetPendingInstantOfferItemPath(pendingOfferId string) string {
	return fmt.Sprintf("%s/%s", GetPendingInstantOfferPath(), pendingOfferId)
}

// Firebase
func GetNotificationInstantOfferItemPath(userId string, offerId string) string {
	return fmt.Sprintf("users/%s/offers/instant_%s", userId, offerId)
}

func snapshotToCCTransaction(snapshot *firestore.DocumentSnapshot) interface{} {
	var obj bean.CCTransaction
	snapshot.DataTo(&obj)
	obj.Id = snapshot.Ref.ID

	return obj
}

func snapshotToInstantOffer(snapshot *firestore.DocumentSnapshot) interface{} {
	var obj bean.InstantOffer
	snapshot.DataTo(&obj)
	obj.Id = snapshot.Ref.ID

	return obj
}
