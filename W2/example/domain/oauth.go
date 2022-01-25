package domain

import (
	"github.com/kokizzu/gotro/L"
	"github.com/kokizzu/gotro/M"
	"github.com/kokizzu/gotro/S"
	"github.com/kokizzu/gotro/W2/example/conf"
	"github.com/kokizzu/gotro/W2/example/model/mAuth/rqAuth"
	"github.com/kokizzu/gotro/W2/example/model/mAuth/wcAuth"
	"github.com/kokizzu/gotro/X"
	"github.com/kokizzu/id64"
	"github.com/kokizzu/lexid"
	"io/ioutil"
	"net/http"
)

//go:generate gomodifytags -all -add-tags json,form,query,long,msg -transform camelcase --skip-unexported -w -file oauth.go
//go:generate replacer 'Id" form' 'Id,string" form' type oauth.go
//go:generate replacer 'json:"id"' 'json:id,string" form' type oauth.go
//go:generate replacer 'By" form' 'By,string" form' type oauth.go
// go:generate msgp -tests=false -file oauth.go -o oauth__MSG.GEN.go

const (
	Google = `google`
)

type (
	UserExternalLogin_In struct {
		RequestCommon
		Provider string `json:"provider" form:"provider" query:"provider" long:"provider" msg:"provider"`
	}
	UserExternalLogin_Out struct {
		ResponseCommon
		Link string `json:"link" form:"link" query:"link" long:"link" msg:"link"`
	}
)

const UserExternalLogin_Url = `/UserExternalLogin`

func (d *Domain) UserExternalLogin(in *UserExternalLogin_In) (out UserExternalLogin_Out) {
	out.SessionToken = lexid.ID()
	csrfState := in.Provider + `|` + out.SessionToken

	switch in.Provider {
	case Google:
		gProvider := conf.GPLUS_OAUTH_PROVIDERS[in.Host]
		if gProvider == nil {
			out.SetError(500, `host not configured with oauth`)
			return
		}
		out.Link = gProvider.AuthCodeURL(csrfState)
		//fmt.Println(out.Link)
	default:
		out.SetError(400, `provider not set`)
	}
	return
}

func fetchJson(client *http.Client, url string, res *ResponseCommon) (json M.SX) {
	resp, err := client.Get(url)
	if L.IsError(err, `failed fetch url %s`, url) {
		res.SetError(500, `failed fetch url`)
		return
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if L.IsError(err, `failed read body`) {
		res.SetError(500, `failed read body`)
		return
	}
	bodyStr := string(body)
	json = S.JsonToMap(bodyStr)
	L.Describe(json)
	err2 := json.GetStr(`error`)
	if L.CheckIf(err2 != ``, `fetchJson %s: %#v`, err2, json) {
		res.SetError(500, `error key set from json response`)
		return
	}
	err3 := json.GetStr(`type`)
	if L.CheckIf(err3 == `OAuthException`, `fetchJson %s: %#v`, err3, json) {
		res.SetError(500, `object type from json respons is OAuthException`)
		return
	}
	return
}

type (
	UserOauth_In struct {
		RequestCommon
		State string `json:"state" form:"state" query:"state" long:"state" msg:"state"`
		Code  string `json:"code" form:"code" query:"code" long:"code" msg:"code"`
	}
	UserOauth_Out struct {
		ResponseCommon
		OauthUser   M.SX         `json:"oauthUser" form:"oauthUser" query:"oauthUser" long:"oauthUser" msg:"oauthUser"`
		Email       string       `json:"email" form:"email" query:"email" long:"email" msg:"email"`
		CurrentUser rqAuth.Users `json:"currentUser" form:"currentUser" query:"currentUser" long:"currentUser" msg:"currentUser"`
	}
)

const UserOauth_Url = `/UserOauth`

func (d *Domain) UserOauth(in *UserOauth_In) (out UserOauth_Out) {
	state := S.Split(in.State, `|`)
	if len(state) < 2 || state[1] != in.SessionToken {
		out.SetError(400, `invalid CSRF oauth state`)
		return
	}
	provider := state[0]
	switch provider {
	case Google:
		gProvider := conf.GPLUS_OAUTH_PROVIDERS[in.Host]
		if gProvider == nil {
			out.SetError(500, `host not configured with oauth`)
			return
		}
		token, err := gProvider.Exchange(in.TracerContext, in.Code)
		if err != nil {
			out.SetError(500, `failed exchange oauth token`)
			return
		}
		client := gProvider.Client(in.TracerContext, token)
		if conf.GPLUS_USERINFO_ENDPOINT == `` {
			// no need to refetch userinfo_endpoint
			json := fetchJson(client, `https://accounts.google.com/.well-known/openid-configuration`, &out.ResponseCommon)
			conf.GPLUS_USERINFO_ENDPOINT = json.GetStr(`userinfo_endpoint`)
			if out.HasError() {
				return
			}
		}
		out.OauthUser = fetchJson(client, conf.GPLUS_USERINFO_ENDPOINT, &out.ResponseCommon)
		// example: {"email":"","email_verified":true,"family_name":"","gender":"","given_name":"","locale":"en-GB","name":"","picture":"http://","profile":"http://","sub":"number"};

		out.Email = out.OauthUser.GetStr(`email`)
		if out.HasError() {
			return
		}
	default:
		out.SetError(400, `provider not set`)
		return
	}

	if out.Email == `` {
		out.SetError(500, `missing email from oauth provider`)
		return
	}

	// login
	user := wcAuth.NewUsersMutator(d.Taran)
	user.Email = out.Email

	if !user.FindByEmail() {

		// force register anyway
		user.Id = id64.UID()
		if !user.SetEncryptPassword(X.ToS(user.Id)) {
			out.SetError(500, `cannot encrypt password`)
			return
		}
		if !user.DoInsert() {
			out.SetError(451, `failed to register this user: `+out.Email)
			return
		}

	}

	d.expireSession(in.SessionToken)

	// create session
	session := d.createSession(user.Id, user.Email, in.UserAgent)
	if !session.DoInsert() {
		out.SetError(500, `cannot create session`)
		return
	}
	out.SessionToken = session.SessionToken

	out.CurrentUser = user.Users

	return
}