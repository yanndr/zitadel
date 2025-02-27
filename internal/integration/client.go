package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zitadel/logging"
	"github.com/zitadel/oidc/v2/pkg/oidc"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"

	"github.com/zitadel/zitadel/internal/api/authz"
	"github.com/zitadel/zitadel/internal/command"
	openid "github.com/zitadel/zitadel/internal/idp/providers/oidc"
	"github.com/zitadel/zitadel/internal/repository/idp"
	"github.com/zitadel/zitadel/pkg/grpc/admin"
	mgmt "github.com/zitadel/zitadel/pkg/grpc/management"
	object "github.com/zitadel/zitadel/pkg/grpc/object/v2alpha"
	session "github.com/zitadel/zitadel/pkg/grpc/session/v2alpha"
	"github.com/zitadel/zitadel/pkg/grpc/system"
	user "github.com/zitadel/zitadel/pkg/grpc/user/v2alpha"
)

type Client struct {
	CC        *grpc.ClientConn
	Admin     admin.AdminServiceClient
	Mgmt      mgmt.ManagementServiceClient
	UserV2    user.UserServiceClient
	SessionV2 session.SessionServiceClient
	System    system.SystemServiceClient
}

func newClient(cc *grpc.ClientConn) Client {
	return Client{
		CC:        cc,
		Admin:     admin.NewAdminServiceClient(cc),
		Mgmt:      mgmt.NewManagementServiceClient(cc),
		UserV2:    user.NewUserServiceClient(cc),
		SessionV2: session.NewSessionServiceClient(cc),
		System:    system.NewSystemServiceClient(cc),
	}
}

func (t *Tester) UseIsolatedInstance(iamOwnerCtx, systemCtx context.Context) (primaryDomain, instanceId string, authenticatedIamOwnerCtx context.Context) {
	primaryDomain = randString(5) + ".integration"
	instance, err := t.Client.System.CreateInstance(systemCtx, &system.CreateInstanceRequest{
		InstanceName: "testinstance",
		CustomDomain: primaryDomain,
		Owner: &system.CreateInstanceRequest_Machine_{
			Machine: &system.CreateInstanceRequest_Machine{
				UserName:            "owner",
				Name:                "owner",
				PersonalAccessToken: &system.CreateInstanceRequest_PersonalAccessToken{},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	t.createClientConn(iamOwnerCtx, grpc.WithAuthority(primaryDomain))
	instanceId = instance.GetInstanceId()
	t.Users[instanceId] = map[UserType]User{
		IAMOwner: {
			Token: instance.GetPat(),
		},
	}
	return primaryDomain, instanceId, t.WithInstanceAuthorization(iamOwnerCtx, IAMOwner, instanceId)
}

func (s *Tester) CreateHumanUser(ctx context.Context) *user.AddHumanUserResponse {
	resp, err := s.Client.UserV2.AddHumanUser(ctx, &user.AddHumanUserRequest{
		Organisation: &object.Organisation{
			Org: &object.Organisation_OrgId{
				OrgId: s.Organisation.ID,
			},
		},
		Profile: &user.SetHumanProfile{
			FirstName: "Mickey",
			LastName:  "Mouse",
		},
		Email: &user.SetHumanEmail{
			Email: fmt.Sprintf("%d@mouse.com", time.Now().UnixNano()),
			Verification: &user.SetHumanEmail_ReturnCode{
				ReturnCode: &user.ReturnEmailVerificationCode{},
			},
		},
	})
	logging.OnError(err).Fatal("create human user")
	return resp
}

func (s *Tester) CreateUserIDPlink(ctx context.Context, userID, externalID, idpID, username string) *user.AddIDPLinkResponse {
	resp, err := s.Client.UserV2.AddIDPLink(
		ctx,
		&user.AddIDPLinkRequest{
			UserId: userID,
			IdpLink: &user.IDPLink{
				IdpId:    idpID,
				UserId:   externalID,
				UserName: username,
			},
		},
	)
	logging.OnError(err).Fatal("create human user link")
	return resp
}

func (s *Tester) RegisterUserPasskey(ctx context.Context, userID string) {
	reg, err := s.Client.UserV2.CreatePasskeyRegistrationLink(ctx, &user.CreatePasskeyRegistrationLinkRequest{
		UserId: userID,
		Medium: &user.CreatePasskeyRegistrationLinkRequest_ReturnCode{},
	})
	logging.OnError(err).Fatal("create user passkey")

	pkr, err := s.Client.UserV2.RegisterPasskey(ctx, &user.RegisterPasskeyRequest{
		UserId: userID,
		Code:   reg.GetCode(),
		Domain: s.Config.ExternalDomain,
	})
	logging.OnError(err).Fatal("create user passkey")
	attestationResponse, err := s.WebAuthN.CreateAttestationResponse(pkr.GetPublicKeyCredentialCreationOptions())
	logging.OnError(err).Fatal("create user passkey")

	_, err = s.Client.UserV2.VerifyPasskeyRegistration(ctx, &user.VerifyPasskeyRegistrationRequest{
		UserId:              userID,
		PasskeyId:           pkr.GetPasskeyId(),
		PublicKeyCredential: attestationResponse,
		PasskeyName:         "nice name",
	})
	logging.OnError(err).Fatal("create user passkey")
}

func (s *Tester) AddGenericOAuthProvider(t *testing.T) string {
	ctx := authz.WithInstance(context.Background(), s.Instance)
	id, _, err := s.Commands.AddOrgGenericOAuthProvider(ctx, s.Organisation.ID, command.GenericOAuthProvider{
		Name:                  "idp",
		ClientID:              "clientID",
		ClientSecret:          "clientSecret",
		AuthorizationEndpoint: "https://example.com/oauth/v2/authorize",
		TokenEndpoint:         "https://example.com/oauth/v2/token",
		UserEndpoint:          "https://api.example.com/user",
		Scopes:                []string{"openid", "profile", "email"},
		IDAttribute:           "id",
		IDPOptions: idp.Options{
			IsLinkingAllowed:  true,
			IsCreationAllowed: true,
			IsAutoCreation:    true,
			IsAutoUpdate:      true,
		},
	})
	require.NoError(t, err)
	return id
}

func (s *Tester) CreateIntent(t *testing.T, idpID string) string {
	ctx := authz.WithInstance(context.Background(), s.Instance)
	id, _, err := s.Commands.CreateIntent(ctx, idpID, "https://example.com/success", "https://example.com/failure", s.Organisation.ID)
	require.NoError(t, err)
	return id
}

func (s *Tester) CreateSuccessfulIntent(t *testing.T, idpID, userID, idpUserID string) (string, string, time.Time, uint64) {
	ctx := authz.WithInstance(context.Background(), s.Instance)
	intentID := s.CreateIntent(t, idpID)
	writeModel, err := s.Commands.GetIntentWriteModel(ctx, intentID, s.Organisation.ID)
	require.NoError(t, err)
	idpUser := openid.NewUser(
		&oidc.UserInfo{
			Subject: idpUserID,
			UserInfoProfile: oidc.UserInfoProfile{
				PreferredUsername: "username",
			},
		},
	)
	idpSession := &openid.Session{
		Tokens: &oidc.Tokens[*oidc.IDTokenClaims]{
			Token: &oauth2.Token{
				AccessToken: "accessToken",
			},
			IDToken: "idToken",
		},
	}
	token, err := s.Commands.SucceedIDPIntent(ctx, writeModel, idpUser, idpSession, userID)
	require.NoError(t, err)
	return intentID, token, writeModel.ChangeDate, writeModel.ProcessedSequence
}
