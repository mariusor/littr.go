package frontend

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	ap "github.com/go-ap/activitypub"
	cl "github.com/go-ap/activitypub/client"
	as "github.com/go-ap/activitystreams"
	"github.com/go-ap/auth"
	"github.com/go-ap/errors"
	j "github.com/go-ap/jsonld"
	local "github.com/mariusor/littr.go/activitypub"
	"github.com/mariusor/littr.go/app"
	"github.com/mariusor/littr.go/internal/log"
	"github.com/mariusor/qstring"
	"github.com/spacemonkeygo/httpsig"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type repository struct {
	BaseURL string
	logger  log.Logger
	client  cl.HttpClient
}

// Repository middleware
func (h handler) Repository(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), app.RepositoryCtxtKey, h.storage)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
	return http.HandlerFunc(fn)
}

func NewRepository(c Config) *repository {
	as.ItemTyperFunc = local.JSONGetItemByType
	cl.UserAgent = fmt.Sprintf("%s-%s", app.Instance.HostName, app.Instance.Version)
	cl.ErrorLogger = func(el ...interface{}) { c.Logger.WithContext(log.Ctx{"client": "api"}).Errorf("%v", el) }
	cl.InfoLogger = func(el ...interface{}) { c.Logger.WithContext(log.Ctx{"client": "api"}).Debugf("%v", el) }

	BaseURL = c.APIURL
	ActorsURL = fmt.Sprintf("%s/actors", BaseURL)
	ObjectsURL = fmt.Sprintf("%s/objects", BaseURL)

	return &repository{
		BaseURL: c.APIURL,
		logger:  c.Logger,
		client:  cl.NewClient(),
	}
}

func getObjectType(el as.Item) string {
	if el == nil {
		return ""
	}
	var label = ""
	switch el.(type) {
	case *ap.Outbox:
		label = "outbox"
	case ap.Outbox:
		label = "outbox"
	case *ap.Inbox:
		label = "inbox"
	case ap.Inbox:
		label = "inbox"
	case ap.Liked:
		label = "liked"
	case *ap.Liked:
		label = "liked"
	case ap.Followers:
		label = "followers"
	case *ap.Followers:
		label = "followers"
	case ap.Following:
		label = "following"
	case *ap.Following:
		label = "following"
	case as.Person:
		if o, ok := el.(as.Person); ok {
			label = o.Name.First().Value
		}
	case *as.Person:
		if o, ok := el.(*as.Person); ok {
			label = o.Name.First().Value
		}
	case ap.Person:
		if o, ok := el.(ap.Person); ok {
			label = o.Name.First().Value
		}
	case *ap.Person:
		if o, ok := el.(*ap.Person); ok {
			label = o.Name.First().Value
		}
	}
	return label
}

func BuildCollectionID(a app.Account, o as.Item) as.ObjectID {
	if len(a.Handle) > 0 {
		return as.ObjectID(fmt.Sprintf("%s/%s/%s", ActorsURL, url.PathEscape(a.Hash.String()), getObjectType(o)))
	}
	return as.ObjectID(fmt.Sprintf("%s/%s", BaseURL, getObjectType(o)))
}

var BaseURL = "http://fedbox.git"
var ActorsURL = fmt.Sprintf("%s/actors", BaseURL)
var ObjectsURL = fmt.Sprintf("%s/objects", BaseURL)

func apAccountID(a app.Account) as.ObjectID {
	if len(a.Hash) >= 8 {
		return as.ObjectID(fmt.Sprintf("%s/%s", ActorsURL, a.Hash.String()))
	}
	return as.ObjectID(fmt.Sprintf("%s/anonymous", ActorsURL))
}

func accountURL(acc app.Account) as.IRI {
	return as.IRI(fmt.Sprintf("%s%s", app.Instance.BaseURL, AccountPermaLink(acc)))
}

func BuildObjectIDFromItem(i app.Item) (as.ObjectID, bool) {
	if !i.IsValid() {
		return "", false
	}
	if i.HasMetadata() && len(i.Metadata.ID) > 0 {
		return as.ObjectID(i.Metadata.ID), true
	}
	return as.ObjectID(fmt.Sprintf("%s/%s", ObjectsURL, url.PathEscape(i.Hash.String()))), true
}

func BuildActorID(a app.Account) as.ObjectID {
	if !a.IsValid() {
		return as.ObjectID(as.PublicNS)
	}
	if a.HasMetadata() && len(a.Metadata.ID) > 0 {
		return as.ObjectID(a.Metadata.ID)
	}
	return as.ObjectID(fmt.Sprintf("%s/%s", ActorsURL, url.PathEscape(a.Hash.String())))
}

func loadAPItem(item app.Item) as.Item {
	o := local.Object{}

	if id, ok := BuildObjectIDFromItem(item); ok {
		o.ID = id
	}
	if item.MimeType == app.MimeTypeURL {
		o.Type = as.PageType
		o.URL = as.IRI(item.Data)
	} else {
		wordCount := strings.Count(item.Data, " ") +
			strings.Count(item.Data, "\t") +
			strings.Count(item.Data, "\n") +
			strings.Count(item.Data, "\r\n")
		if wordCount > 300 {
			o.Type = as.ArticleType
		} else {
			o.Type = as.NoteType
		}

		if len(item.Hash) > 0 {
			o.URL = as.IRI(ItemPermaLink(item))
		}
		o.Name = make(as.NaturalLanguageValues, 0)
		switch item.MimeType {
		case app.MimeTypeMarkdown:
			o.Object.Source.MediaType = as.MimeType(item.MimeType)
			o.MediaType = as.MimeType(app.MimeTypeHTML)
			if item.Data != "" {
				o.Source.Content.Set("en", item.Data)
				o.Content.Set("en", string(app.Markdown(item.Data)))
			}
		case app.MimeTypeText:
			fallthrough
		case app.MimeTypeHTML:
			o.MediaType = as.MimeType(item.MimeType)
			o.Content.Set("en", item.Data)
		}
	}

	// TODO(marius): add proper dynamic recipients to this based on some selector in the frontend
	o.To = as.ItemCollection{as.PublicNS}
	o.BCC = as.ItemCollection{as.IRI(BaseURL)}
	o.Published = item.SubmittedAt
	o.Updated = item.UpdatedAt

	if item.Deleted() {
		del := as.Tombstone{
			Parent: as.Object{
				ID:   o.ID,
				Type: as.TombstoneType,
			},
			FormerType: o.Type,
			Deleted:    o.Updated,
		}
		repl := make(as.ItemCollection, 0)
		if item.Parent != nil {
			if par, ok := BuildObjectIDFromItem(*item.Parent); ok {
				repl = append(repl, as.IRI(par))
			}
			if item.OP == nil {
				item.OP = item.Parent
			}
		}
		if item.OP != nil {
			if op, ok := BuildObjectIDFromItem(*item.OP); ok {
				iri := as.IRI(op)
				del.Context = iri
				if !repl.Contains(iri) {
					repl = append(repl, iri)
				}
			}
		}
		if len(repl) > 0 {
			del.InReplyTo = repl
		}

		return del
	}

	//o.Generator = as.IRI(app.Instance.BaseURL)
	o.Score = item.Score / app.ScoreMultiplier
	if item.Title != "" {
		o.Name.Set("en", string(item.Title))
	}
	if item.SubmittedBy != nil {
		id := BuildActorID(*item.SubmittedBy)
		o.AttributedTo = as.IRI(id)
	}
	repl := make(as.ItemCollection, 0)
	if item.Parent != nil {
		p := item.Parent
		if par, ok := BuildObjectIDFromItem(*p); ok {
			repl = append(repl, as.IRI(par))
		}
		if p.SubmittedBy.IsValid() {
			if pAuth := BuildActorID(*p.SubmittedBy); as.IRI(pAuth) != as.PublicNS {
				o.To = append(o.To, as.IRI(pAuth))
			}
		}
		if item.OP == nil {
			item.OP = p
		}
	}
	if item.OP != nil {
		if op, ok := BuildObjectIDFromItem(*item.OP); ok {
			iri := as.IRI(op)
			o.Context = iri
			if !repl.Contains(iri) {
				repl = append(repl, iri)
			}
		}
	}
	if len(repl) > 0 {
		o.InReplyTo = repl
	}

	if item.Metadata != nil {
		m := item.Metadata
		if m.Mentions != nil || m.Tags != nil {
			o.Tag = make(as.ItemCollection, 0)
			for _, men := range m.Mentions {
				// todo(marius): retrieve object ids of each mention and add it to the CC of the object
				t := as.Object{
					ID:   as.ObjectID(men.URL),
					Type: as.MentionType,
					Name: as.NaturalLanguageValues{{Ref: as.NilLangRef, Value: men.Name}},
				}
				o.Tag.Append(t)
			}
			for _, tag := range m.Tags {
				t := as.Object{
					ID:   as.ObjectID(tag.URL),
					Type: as.ObjectType,
					Name: as.NaturalLanguageValues{{Ref: as.NilLangRef, Value: tag.Name}},
				}
				o.Tag.Append(t)
			}
		}
	}

	return &o
}

func anonymousPerson(url string) *auth.Person {
	p := auth.Person{}
	p.ID = as.ObjectID(as.PublicNS)
	p.Type = as.PersonType

	name := as.NaturalLanguageValues{
		{as.NilLangRef, app.Anonymous},
	}

	p.Name = name
	p.PreferredUsername = name

	p.Inbox = as.IRI(fmt.Sprintf("%s/inbox", url))

	return &p
}

func loadAPPerson(a app.Account) *auth.Person {
	p := auth.Person{}
	p.Type = as.PersonType
	p.Name = as.NaturalLanguageValuesNew()
	p.PreferredUsername = as.NaturalLanguageValuesNew()

	if a.HasMetadata() {
		if a.Metadata.Blurb != nil && len(a.Metadata.Blurb) > 0 {
			p.Summary = as.NaturalLanguageValuesNew()
			p.Summary.Set(as.NilLangRef, string(a.Metadata.Blurb))
		}
		if len(a.Metadata.Icon.URI) > 0 {
			avatar := as.ObjectNew(as.ImageType)
			avatar.MediaType = as.MimeType(a.Metadata.Icon.MimeType)
			avatar.URL = as.IRI(a.Metadata.Icon.URI)
			p.Icon = avatar
		}
	}

	p.PreferredUsername.Set(as.NilLangRef, a.Handle)

	if len(a.Hash) > 0 {
		if a.IsFederated() {
			p.ID = as.ObjectID(a.Metadata.ID)
			p.Name.Set("en", a.Metadata.Name)
			if len(a.Metadata.InboxIRI) > 0 {
				p.Inbox = as.IRI(a.Metadata.InboxIRI)
			}
			if len(a.Metadata.OutboxIRI) > 0 {
				p.Outbox = as.IRI(a.Metadata.OutboxIRI)
			}
			if len(a.Metadata.LikedIRI) > 0 {
				p.Liked = as.IRI(a.Metadata.LikedIRI)
			}
			if len(a.Metadata.FollowersIRI) > 0 {
				p.Followers = as.IRI(a.Metadata.FollowersIRI)
			}
			if len(a.Metadata.FollowingIRI) > 0 {
				p.Following = as.IRI(a.Metadata.FollowingIRI)
			}
			if len(a.Metadata.URL) > 0 {
				p.URL = as.IRI(a.Metadata.URL)
			}
		} else {
			p.Name.Set("en", a.Handle)

			p.Outbox = as.IRI(BuildCollectionID(a, new(ap.Outbox)))
			p.Inbox = as.IRI(BuildCollectionID(a, new(ap.Inbox)))
			p.Liked = as.IRI(BuildCollectionID(a, new(ap.Liked)))

			p.URL = accountURL(a)

			if !a.CreatedAt.IsZero() {
				p.Published = a.CreatedAt
			}
			if !a.UpdatedAt.IsZero() {
				p.Updated = a.UpdatedAt
			}
		}
		if len(a.Hash) >= 8 {
			p.ID = apAccountID(a)
		}
		oauthURL := strings.Replace(BaseURL, "api", "oauth", 1)
		p.Endpoints = &ap.Endpoints{
			SharedInbox:                as.IRI(fmt.Sprintf("%s/inbox", BaseURL)),
			OauthAuthorizationEndpoint: as.IRI(fmt.Sprintf("%s/authorize", oauthURL)),
			OauthTokenEndpoint:         as.IRI(fmt.Sprintf("%s/token", oauthURL)),
		}
	}

	//p.Score = a.Score
	if a.IsValid() && a.HasMetadata() && a.Metadata.Key != nil && a.Metadata.Key.Public != nil {
		p.PublicKey = auth.PublicKey{
			ID:           as.ObjectID(fmt.Sprintf("%s#main-key", p.ID)),
			Owner:        as.IRI(p.ID),
			PublicKeyPem: fmt.Sprintf("-----BEGIN PUBLIC KEY-----\n%s\n-----END PUBLIC KEY-----", base64.StdEncoding.EncodeToString(a.Metadata.Key.Public)),
		}
	}
	return &p
}

type SignFunc func(r *http.Request) error

func getSigner(pubKeyID as.ObjectID, key crypto.PrivateKey) *httpsig.Signer {
	hdrs := []string{"(request-target)", "host", "date"}
	return httpsig.NewSigner(string(pubKeyID), key, httpsig.RSASHA256, hdrs)
}

func (r *repository) WithAccount(a *app.Account) error {
	// TODO(marius): this needs to be added to the federated requests, which we currently don't support
	if !a.IsValid() || !a.IsLogged() {
		return nil
	}

	r.client.SignFn(func(r *http.Request) error {
		if a.Metadata.OAuth.Token == "" {
			return nil
		}
		r.Header.Add("Authorization", fmt.Sprintf("Bearer %s", a.Metadata.OAuth.Token))
		return nil
	})
	return nil
}

func (r *repository) withAccountS2S(a *app.Account) error {
	// TODO(marius): this needs to be added to the federated requests, which we currently don't support

	if !a.IsValid() || !a.IsLogged() {
		return nil
	}

	k := a.Metadata.Key
	if k == nil {
		return nil
	}
	var prv crypto.PrivateKey
	var err error
	if k.ID == "id-rsa" {
		prv, err = x509.ParsePKCS8PrivateKey(k.Private)
	}
	if err != nil {
		return err
	}
	if k.ID == "id-ecdsa" {
		return errors.Errorf("unsupported private key type %s", k.ID)
		//prv, err = x509.ParseECPrivateKey(k.Private)
	}
	if err != nil {
		return err
	}
	p := *loadAPPerson(*a)
	s := getSigner(p.PublicKey.ID, prv)
	r.client.SignFn(s.Sign)

	return nil
}

func (r *repository) LoadItem(f app.Filters) (app.Item, error) {
	var item app.Item

	f.MaxItems = 1
	hashes := f.LoadItemsFilter.Key
	f.LoadItemsFilter.Key = nil

	url := fmt.Sprintf("%s/objects/%s", r.BaseURL, hashes[0])
	it, err := r.client.LoadIRI(as.IRI(url))
	if err != nil {
		r.logger.Error(err.Error())
		return item, err
	}
	local.OnObject(it, func(art *local.Object) error {
		err = item.FromActivityPub(art)
		if err == nil {
			var items app.ItemCollection
			items, err = r.loadItemsAuthors(item)
			items, err = r.loadItemsVotes(items...)
			if len(items) > 0 {
				item = items[0]
			}
		}
		return nil
	})

	return item, err
}

func hashesUnique(a app.Hashes) app.Hashes {
	u := make([]app.Hash, 0, len(a))
	m := make(map[string]bool)

	for _, val := range a {
		k := val.String()
		if _, ok := m[k]; !ok {
			m[k] = true
			u = append(u, val)
		}
	}
	return u
}

func (r *repository) loadAccountsVotes(accounts ...app.Account) (app.AccountCollection, error) {
	if len(accounts) == 0 {
		return accounts, nil
	}
	fVotes := app.Filters{}
	for _, account := range accounts {
		fVotes.LoadVotesFilter.AttributedTo = append(fVotes.LoadVotesFilter.AttributedTo, account.Hash)
	}
	fVotes.LoadVotesFilter.AttributedTo = hashesUnique(fVotes.LoadVotesFilter.AttributedTo)
	col := make(app.AccountCollection, len(accounts))
	votes, _, err := r.LoadVotes(fVotes)
	if err != nil {
		return nil, errors.Annotatef(err, "unable to load accounts votes")
	}
	for k, ac := range accounts {
		for _, vot := range votes {
			if bytes.Equal(vot.SubmittedBy.Hash, ac.Hash) {
				ac.Score += vot.Weight
			}
		}
		col[k] = ac
	}
	return col, nil
}

func (r *repository) loadItemsVotes(items ...app.Item) (app.ItemCollection, error) {
	if len(items) == 0 {
		return items, nil
	}
	fVotes := app.Filters{}
	for _, it := range items {
		fVotes.LoadVotesFilter.ItemKey = append(fVotes.LoadVotesFilter.ItemKey, it.Hash)
	}
	fVotes.LoadVotesFilter.ItemKey = hashesUnique(fVotes.LoadVotesFilter.ItemKey)
	col := make(app.ItemCollection, len(items))
	votes, _, err := r.LoadVotes(fVotes)
	if err != nil {
		return items, errors.Annotatef(err, "unable to load items votes")
	}
	for k, it := range items {
		for _, vot := range votes {
			if bytes.Equal(vot.Item.Hash, it.Hash) {
				it.Score += vot.Weight
			}
		}
		col[k] = it
	}
	return col, nil
}

func (r *repository) loadItemsAuthors(items ...app.Item) (app.ItemCollection, error) {
	if len(items) == 0 {
		return items, nil
	}

	fActors := app.Filters{}
	for _, it := range items {
		if !it.SubmittedBy.IsValid() {
			continue
		}
		if it.SubmittedBy.HasMetadata() && len(it.SubmittedBy.Metadata.ID) > 0 {
			fActors.LoadAccountsFilter.Key = append(fActors.LoadAccountsFilter.Key, app.Hash(it.SubmittedBy.Metadata.ID))
		} else if len(it.SubmittedBy.Hash) > 0 {
			fActors.LoadAccountsFilter.Key = append(fActors.LoadAccountsFilter.Key, it.SubmittedBy.Hash)
		} else if len(it.SubmittedBy.Handle) > 0 {
			fActors.Handle = append(fActors.Handle, it.SubmittedBy.Handle)
		}
	}
	fActors.LoadAccountsFilter.Key = hashesUnique(fActors.LoadAccountsFilter.Key)
	if len(fActors.LoadAccountsFilter.Key)+len(fActors.Handle) == 0 {
		return items, errors.Errorf("unable to load items authors")
	}
	col := make(app.ItemCollection, len(items))
	authors, _, err := r.LoadAccounts(fActors)
	if err != nil {
		return items, errors.Annotatef(err, "unable to load items authors")
	}
	for k, it := range items {
		for _, auth := range authors {
			if bytes.Equal(it.SubmittedBy.Hash, auth.Hash) || it.SubmittedBy.Handle == auth.Handle {
				it.SubmittedBy = &auth
				break
			}
			if it.UpdatedBy != nil {
				if bytes.Equal(it.UpdatedBy.Hash, auth.Hash) || it.UpdatedBy.Handle == auth.Handle {
					it.UpdatedBy = &auth
					break
				}
			}
		}
		col[k] = it
	}
	return col, nil
}

func (r *repository) LoadItems(f app.Filters) (app.ItemCollection, uint, error) {
	var qs string

	target := "/"
	c := "objects"
	if len(f.FollowedBy) > 0 {
		// TODO(marius): make this work for multiple FollowedBy filters
		target = fmt.Sprintf("/actors/%s/", f.FollowedBy)
		c = "inbox"
		f.FollowedBy = ""
		f.LoadItemsFilter.IRI = ""
		f.Federated = nil
		f.InReplyTo = nil
		f.Type = as.ActivityVocabularyTypes{
			as.CreateType,
		}
	}
	if len(f.Federated) > 0 {
		for _, fed := range f.Federated {
			if !fed {
				// TODO(marius): need to add to fedbox support for filtering by hostname
				f.LoadItemsFilter.IRI = BaseURL
				break
			}
		}
	}
	if len(f.Type) == 0 && len(f.LoadItemsFilter.Deleted) > 0 {
		f.Type = as.ActivityVocabularyTypes{
			as.ArticleType,
			as.AudioType,
			as.DocumentType,
			as.ImageType,
			as.NoteType,
			as.PageType,
			as.VideoType,
		}
	}
	if q, err := qstring.MarshalString(&f); err == nil {
		qs = fmt.Sprintf("?%s", q)
	}
	url := fmt.Sprintf("%s%s%s%s", r.BaseURL, target, c, qs)

	ctx := log.Ctx{
		"url": url,
	}

	it, err := r.client.LoadIRI(as.IRI(url))
	if err != nil {
		r.logger.WithContext(ctx).Error(err.Error())
		return nil, 0, err
	}

	items := make(app.ItemCollection, 0)
	var count uint = 0
	ap.OnOrderedCollection(it, func(col *as.OrderedCollection) error {
		count = col.TotalItems
		for _, it := range col.OrderedItems {
			i := app.Item{}
			if err := i.FromActivityPub(it); err != nil {
				r.logger.Error(err.Error())
				continue
			}
			items = append(items, i)
		}
		return nil
	})

	// TODO(marius): move this somewhere more palatable
	//  it's currently done like this when loading from collections of Activities that only contain the ID of the object
	toLoad := make(app.Hashes, 0)
	for _, i := range items {
		if i.IsValid() && i.SubmittedAt.IsZero() {
			toLoad = append(toLoad, app.Hash(i.Metadata.ID))
		}
	}
	if len(toLoad) > 0 {
		return r.LoadItems(app.Filters{
			LoadItemsFilter: app.LoadItemsFilter{
				Key: toLoad,
			},
		})
	}
	items, err = r.loadItemsAuthors(items...)
	items, err = r.loadItemsVotes(items...)

	return items, count, err
}

func (r *repository) SaveVote(v app.Vote) (app.Vote, error) {
	if !v.SubmittedBy.IsValid() || !v.SubmittedBy.HasMetadata() {
		return app.Vote{}, errors.Newf("Invalid vote submitter")
	}
	if !v.Item.IsValid() || !v.Item.HasMetadata() {
		return app.Vote{}, errors.Newf("Invalid vote item")
	}
	author := loadAPPerson(*v.SubmittedBy)
	reqURL := r.getAuthorRequestURL(v.SubmittedBy)

	url := fmt.Sprintf("%s/%s", v.Item.Metadata.ID, "likes")
	itemVotes, err := r.loadVotesCollection(as.IRI(url), as.IRI(v.SubmittedBy.Metadata.ID))
	// first step is to verify if vote already exists:
	if err != nil {
		r.logger.WithContext(log.Ctx{
			"url": url,
			"err": err,
		}).Warn(err.Error())
	}
	var exists app.Vote
	for _, vot := range itemVotes {
		if !vot.SubmittedBy.IsValid() || !v.SubmittedBy.IsValid() {
			continue
		}
		if bytes.Equal(vot.SubmittedBy.Hash, v.SubmittedBy.Hash) {
			exists = vot
			break
		}
	}

	o := loadAPItem(*v.Item)
	act := as.Activity{
		Parent: as.Object{
			Type: as.UndoType,
			To:   as.ItemCollection{as.PublicNS},
			BCC:  as.ItemCollection{as.IRI(BaseURL)},
		},
		Actor: author.GetLink(),
	}

	if exists.HasMetadata() {
		act.Object = as.IRI(exists.Metadata.IRI)
		// undo old vote
		var body []byte
		if body, err = j.Marshal(act); err != nil {
			r.logger.Error(err.Error())
		}
		var resp *http.Response
		if resp, err = r.client.Post(reqURL, cl.ContentTypeActivityJson, bytes.NewReader(body)); err != nil {
			r.logger.Error(err.Error())
		}
		if body, err = ioutil.ReadAll(resp.Body); err != nil {
			r.logger.Error(err.Error())
		}
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
			r.logger.Error("unable to undo previous vote")
		}
	}

	if v.Weight > 0 && exists.Weight <= 0 {
		act.Type = as.LikeType
		act.Object = o.GetLink()
	}
	if v.Weight < 0 && exists.Weight >= 0 {
		act.Type = as.DislikeType
		act.Object = o.GetLink()
	}

	var body []byte
	if body, err = j.Marshal(act); err != nil {
		r.logger.Error(err.Error())
		return v, err
	}

	var resp *http.Response
	if resp, err = r.client.Post(reqURL, cl.ContentTypeActivityJson, bytes.NewReader(body)); err != nil {
		r.logger.Error(err.Error())
		return v, err
	}
	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		r.logger.Error(err.Error())
		return v, err
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		err := v.FromActivityPub(act)
		return v, err
	}
	return v, r.handlerErrorResponse(body)
}

func (r *repository) loadVotesCollection(iri as.IRI, actors ...as.IRI) ([]app.Vote, error) {
	cntActors := len(actors)
	if cntActors > 0 {
		attrTo := make([]app.Hash, cntActors)
		for i, a := range actors {
			attrTo[i] = app.Hash(a.String())
		}
		f := app.LoadVotesFilter{
			AttributedTo: attrTo,
		}
		if q, err := qstring.MarshalString(&f); err == nil {
			iri = as.IRI(fmt.Sprintf("%s?%s", iri, q))
		}
	}
	likes, err := r.client.LoadIRI(iri)
	// first step is to verify if vote already exists:
	if err != nil {
		return nil, err
	}
	votes := make([]app.Vote, 0)
	err = ap.OnOrderedCollection(likes, func(col *as.OrderedCollection) error {
		for _, like := range col.OrderedItems {
			vote := app.Vote{}
			vote.FromActivityPub(like)
			votes = append(votes, vote)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return votes, nil
}

func (r *repository) LoadVotes(f app.Filters) (app.VoteCollection, uint, error) {
	var qs string
	f.Type = as.ActivityVocabularyTypes{
		as.LikeType,
		as.DislikeType,
		as.UndoType,
	}

	var url string
	if len(f.LoadVotesFilter.AttributedTo) == 1 {
		attrTo := f.LoadVotesFilter.AttributedTo[0]
		f.LoadVotesFilter.AttributedTo = nil
		if q, err := qstring.MarshalString(&f); err == nil {
			qs = fmt.Sprintf("?%s", q)
		}
		url = fmt.Sprintf("%s/actors/%s/outbox%s", r.BaseURL, attrTo, qs)
	} else {
		if q, err := qstring.MarshalString(&f); err == nil {
			qs = fmt.Sprintf("?%s", q)
		}
		url = fmt.Sprintf("%s/inbox%s", r.BaseURL, qs)
	}

	it, err := r.client.LoadIRI(as.IRI(url))
	if err != nil {
		r.logger.Error(err.Error())
		return nil, 0, err
	}
	var count uint = 0
	votes := make(app.VoteCollection, 0)
	undos := make(app.VoteCollection, 0)
	ap.OnOrderedCollection(it, func(col *as.OrderedCollection) error {
		count = col.TotalItems
		for _, it := range col.OrderedItems {
			vot := app.Vote{}
			if err := vot.FromActivityPub(it); err != nil {
				r.logger.WithContext(log.Ctx{
					"type": fmt.Sprintf("%T", it),
				}).Warn(err.Error())
				continue
			}
			if vot.Weight != 0 {
				votes = append(votes, vot)
			} else {
				if vot.Metadata == nil || len(vot.Metadata.OriginalIRI) == 0 {
					r.logger.Error("Zero vote without an original activity undone")
					continue
				}
				undos = append(undos, vot)
			}
		}
		for _, undo := range undos {
			if undo.Metadata == nil || len(undo.Metadata.OriginalIRI) == 0 {
				// error
				continue
			}
			for i, vot := range votes {
				if vot.Metadata == nil || len(vot.Metadata.IRI) == 0 {
					// error
					continue
				}
				if vot.Metadata.IRI == undo.Metadata.OriginalIRI {
					votes = append(votes[:i], votes[i+1:]...)
				}
			}
		}
		return err
	})

	return votes, count, nil
}

func (r *repository) LoadVote(f app.Filters) (app.Vote, error) {
	if len(f.ItemKey) == 0 {
		return app.Vote{}, errors.Newf("invalid item hash")
	}

	v := app.Vote{}
	itemHash := f.ItemKey[0]
	f.ItemKey = nil
	url := fmt.Sprintf("%s/liked/%s", r.BaseURL, itemHash)

	it, err := r.client.LoadIRI(as.IRI(url))
	if err != nil {
		r.logger.Error(err.Error())
		return v, err
	}
	err = ap.OnActivity(it, func(like *as.Activity) error {
		return v.FromActivityPub(like)
	})
	return v, err
}

type _errors struct {
	Ctxt   string        `jsonld:"@context"`
	Errors []errors.Http `jsonld:"errors"`
}

func (r *repository) handlerErrorResponse(body []byte) error {
	errs := _errors{}
	if err := j.Unmarshal(body, &errs); err != nil {
		r.logger.Errorf("Unable to unmarshall error response: %s", err.Error())
		return nil
	}
	if len(errs.Errors) == 0 {
		return nil
	}
	err := errs.Errors[0]
	return errors.WrapWithStatus(err.Code, nil, err.Message)
}

func (r *repository) handleItemSaveSuccessResponse(it app.Item, body []byte) (app.Item, error) {
	ap, err := as.UnmarshalJSON(body)
	if err != nil {
		r.logger.Error(err.Error())
		return it, err
	}
	err = it.FromActivityPub(ap)
	if err != nil {
		r.logger.Error(err.Error())
		return it, err
	}
	items, err := r.loadItemsAuthors(it)
	return items[0], err
}

func (r *repository) getAuthorRequestURL(a *app.Account) string {
	var reqURL string
	if a.IsValid() && a.IsLogged() {
		author := loadAPPerson(*a)
		if a.IsLocal() {
			reqURL = author.Outbox.GetLink().String()
		} else {
			reqURL = author.Inbox.GetLink().String()
		}
	} else {
		author := anonymousPerson(r.BaseURL)
		reqURL = author.Inbox.GetLink().String()
	}
	return reqURL
}

func (r *repository) SaveItem(it app.Item) (app.Item, error) {
	if !it.SubmittedBy.IsValid() || !it.SubmittedBy.HasMetadata() {
		return app.Item{}, errors.Newf("Invalid item submitter")
	}
	art := loadAPItem(it)
	author := loadAPPerson(*it.SubmittedBy)
	reqURL := r.getAuthorRequestURL(it.SubmittedBy)

	var body []byte
	var err error
	id := art.GetLink()
	if it.Deleted() {
		if len(id) == 0 {
			r.logger.WithContext(log.Ctx{
				"item": it.Hash,
			}).Error(err.Error())
			return it, errors.NotFoundf("item hash is empty, can not delete")
		}
		delete := as.Delete{
			Parent: as.Parent{
				Type: as.DeleteType,
				To:   as.ItemCollection{as.PublicNS},
				BCC:  as.ItemCollection{as.IRI(BaseURL)},
			},
			Actor:  author.GetLink(),
			Object: id,
		}
		body, err = j.Marshal(delete)
	} else {
		if len(id) == 0 {
			create := as.Create{
				Parent: as.Parent{
					Type: as.CreateType,
					To:   as.ItemCollection{as.PublicNS},
					BCC:  as.ItemCollection{as.IRI(BaseURL)},
				},
				Actor:  author.GetLink(),
				Object: art,
			}
			body, err = j.Marshal(create)
		} else {
			update := as.Update{
				Parent: as.Parent{
					Type: as.UpdateType,
					To:   as.ItemCollection{as.PublicNS},
					BCC:  as.ItemCollection{as.IRI(BaseURL)},
				},
				Object: art,
				Actor:  author.GetLink(),
			}
			body, err = j.Marshal(update)
		}
	}

	if err != nil {
		r.logger.WithContext(log.Ctx{
			"item": it.Hash,
		}).Error(err.Error())
		return it, err
	}
	var resp *http.Response
	resp, err = r.client.Post(reqURL, cl.ContentTypeActivityJson, bytes.NewReader(body))
	if err != nil {
		r.logger.Error(err.Error())
		return it, err
	}
	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		r.logger.Error(err.Error())
		return it, err
	}
	if resp.StatusCode >= 400 {
		return it, r.handlerErrorResponse(body)
	}
	return r.handleItemSaveSuccessResponse(it, body)
}

func (r *repository) LoadAccounts(f app.Filters) (app.AccountCollection, uint, error) {
	var qs string
	if q, err := qstring.MarshalString(&f); err == nil {
		qs = fmt.Sprintf("?%s", q)
	}
	url := fmt.Sprintf("%s/%s", ActorsURL, qs)

	it, err := r.client.LoadIRI(as.IRI(url))
	if err != nil {
		r.logger.Error(err.Error())
		return nil, 0, err
	}
	accounts := make(app.AccountCollection, 0)
	var count uint = 0
	ap.OnOrderedCollection(it, func(col *as.OrderedCollection) error {
		count = col.TotalItems
		for _, it := range col.OrderedItems {
			acc := app.Account{}
			if err := acc.FromActivityPub(it); err != nil {
				r.logger.WithContext(log.Ctx{
					"type": fmt.Sprintf("%T", it),
				}).Warn(err.Error())
				continue
			}
			accounts = append(accounts, acc)
		}
		accounts, err = r.loadAccountsVotes(accounts...)
		return err
	})
	return accounts, count, nil
}

func (r *repository) LoadAccount(f app.Filters) (app.Account, error) {
	var accounts app.AccountCollection
	var err error
	if accounts, _, err = r.LoadAccounts(f); err != nil {
		return app.Account{}, err
	}

	var ac *app.Account
	if ac, err = accounts.First(); err != nil {
		var id string
		if len(f.LoadAccountsFilter.Key) > 0 {
			id = f.LoadAccountsFilter.Key[0].String()
		}
		if len(f.Handle) > 0 {
			id = f.Handle[0]
		}
		return app.Account{}, errors.NotFoundf("account %s", id)
	}
	return *ac, nil
}

func (r *repository) SaveAccount(a app.Account) (app.Account, error) {
	p := loadAPPerson(a)
	id := p.GetLink()

	p.Generator = as.IRI(app.Instance.BaseURL)
	p.Published = time.Now()
	p.Updated = p.Published
	p.URL = accountURL(a)

	author := loadAPPerson(*a.CreatedBy)
	reqURL := author.Inbox.GetLink().String()

	var body []byte
	var err error
	now := time.Now()
	if a.Deleted() {
		if len(id) == 0 {
			r.logger.WithContext(log.Ctx{
				"account": a.Hash,
			}).Error(err.Error())
			return a, errors.NotFoundf("item hash is empty, can not delete")
		}
		delete := as.Delete{
			Parent: as.Parent{
				Type:         as.DeleteType,
				To:           as.ItemCollection{as.PublicNS},
				BCC:          as.ItemCollection{as.IRI(BaseURL)},
				AttributedTo: author.GetLink(),
				Updated:      now,
			},
			Actor:  author.GetLink(),
			Object: id,
		}
		body, err = j.Marshal(delete)
	} else {
		if len(id) == 0 {
			p.To = as.ItemCollection{as.PublicNS}
			p.BCC = as.ItemCollection{as.IRI(BaseURL)}
			create := as.Create{
				Parent: as.Parent{
					Type:         as.CreateType,
					To:           as.ItemCollection{as.PublicNS},
					BCC:          as.ItemCollection{as.IRI(BaseURL)},
					AttributedTo: author.GetLink(),
					Published:    now,
					Updated:      now,
				},
				Actor:  author.GetLink(),
				Object: p,
			}
			body, err = j.Marshal(create)
		} else {
			update := as.Update{
				Parent: as.Parent{
					Type:         as.UpdateType,
					To:           as.ItemCollection{as.PublicNS},
					BCC:          as.ItemCollection{as.IRI(BaseURL)},
					AttributedTo: author.GetLink(),
					Updated:      now,
				},
				Object: p,
				Actor:  author.GetLink(),
			}
			body, err = j.Marshal(update)
		}
	}
	if err != nil {
		r.logger.WithContext(log.Ctx{
			"account": a.Hash,
		}).Error(err.Error())
		return a, err
	}

	var resp *http.Response
	resp, err = r.client.Post(reqURL, cl.ContentTypeActivityJson, bytes.NewReader(body))
	if err != nil {
		r.logger.Error(err.Error())
		return a, err
	}
	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		r.logger.Error(err.Error())
		return a, err
	}
	if resp.StatusCode >= 400 {
		return a, r.handlerErrorResponse(body)
	}
	ap, err := as.UnmarshalJSON(body)
	if err != nil {
		r.logger.Error(err.Error())
		return a, err
	}
	err = a.FromActivityPub(ap)
	if err != nil {
		r.logger.Error(err.Error())
	}
	return a, err
}

// LoadInfo this method is here to keep compatibility with the repository interfaces
// but in the long term we might want to store some of this information in the DB
func (r *repository) LoadInfo() (app.Info, error) {
	return app.Instance.NodeInfo(), nil
}
