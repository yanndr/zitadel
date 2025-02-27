package user

import (
	"errors"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/muhlemmer/gu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zitadel/zitadel/internal/api/grpc"
	"github.com/zitadel/zitadel/internal/command"
	"github.com/zitadel/zitadel/internal/crypto"
	"github.com/zitadel/zitadel/internal/domain"
	caos_errs "github.com/zitadel/zitadel/internal/errors"
	"github.com/zitadel/zitadel/internal/eventstore"
	object_pb "github.com/zitadel/zitadel/pkg/grpc/object/v2alpha"
	user "github.com/zitadel/zitadel/pkg/grpc/user/v2alpha"
)

var ignoreTypes = []protoreflect.FullName{"google.protobuf.Duration", "google.protobuf.Struct"}

func Test_hashedPasswordToCommand(t *testing.T) {
	type args struct {
		hashed *user.HashedPassword
	}
	type res struct {
		want string
		err  func(error) bool
	}
	tests := []struct {
		name string
		args args
		res  res
	}{
		{
			"not hashed",
			args{
				hashed: nil,
			},
			res{
				"",
				nil,
			},
		},
		{
			"hashed, not bcrypt",
			args{
				hashed: &user.HashedPassword{
					Hash:      "hash",
					Algorithm: "custom",
				},
			},
			res{
				"",
				func(err error) bool {
					return errors.Is(err, caos_errs.ThrowInvalidArgument(nil, "USER-JDk4t", "Errors.InvalidArgument"))
				},
			},
		},
		{
			"hashed, bcrypt",
			args{
				hashed: &user.HashedPassword{
					Hash:      "hash",
					Algorithm: "bcrypt",
				},
			},
			res{
				"hash",
				nil,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := hashedPasswordToCommand(tt.args.hashed)
			if tt.res.err == nil {
				require.NoError(t, err)
			}
			if tt.res.err != nil && !tt.res.err(err) {
				t.Errorf("got wrong err: %v ", err)
			}
			if tt.res.err == nil {
				assert.Equal(t, tt.res.want, got)
			}
		})
	}
}

func Test_intentToIDPInformationPb(t *testing.T) {
	decryption := func(err error) crypto.EncryptionAlgorithm {
		mCrypto := crypto.NewMockEncryptionAlgorithm(gomock.NewController(t))
		mCrypto.EXPECT().Algorithm().Return("enc")
		mCrypto.EXPECT().DecryptionKeyIDs().Return([]string{"id"})
		mCrypto.EXPECT().DecryptString(gomock.Any(), gomock.Any()).DoAndReturn(
			func(code []byte, keyID string) (string, error) {
				if err != nil {
					return "", err
				}
				return string(code), nil
			})
		return mCrypto
	}

	type args struct {
		intent *command.IDPIntentWriteModel
		alg    crypto.EncryptionAlgorithm
	}
	type res struct {
		resp *user.RetrieveIdentityProviderInformationResponse
		err  error
	}
	tests := []struct {
		name string
		args args
		res  res
	}{
		{
			"decryption invalid key id error",
			args{
				intent: &command.IDPIntentWriteModel{
					WriteModel: eventstore.WriteModel{
						AggregateID:       "intentID",
						ProcessedSequence: 123,
						ResourceOwner:     "ro",
						InstanceID:        "instanceID",
						ChangeDate:        time.Date(2019, 4, 1, 1, 1, 1, 1, time.Local),
					},
					IDPID:       "idpID",
					IDPUser:     []byte(`{"userID": "idpUserID", "username": "username"}`),
					IDPUserID:   "idpUserID",
					IDPUserName: "username",
					IDPAccessToken: &crypto.CryptoValue{
						CryptoType: crypto.TypeEncryption,
						Algorithm:  "enc",
						KeyID:      "id",
						Crypted:    []byte("accessToken"),
					},
					IDPIDToken: "idToken",
					UserID:     "userID",
					State:      domain.IDPIntentStateSucceeded,
				},
				alg: decryption(caos_errs.ThrowInternal(nil, "id", "invalid key id")),
			},
			res{
				resp: nil,
				err:  caos_errs.ThrowInternal(nil, "id", "invalid key id"),
			},
		},
		{
			"successful",
			args{
				intent: &command.IDPIntentWriteModel{
					WriteModel: eventstore.WriteModel{
						AggregateID:       "intentID",
						ProcessedSequence: 123,
						ResourceOwner:     "ro",
						InstanceID:        "instanceID",
						ChangeDate:        time.Date(2019, 4, 1, 1, 1, 1, 1, time.Local),
					},
					IDPID:       "idpID",
					IDPUser:     []byte(`{"userID": "idpUserID", "username": "username"}`),
					IDPUserID:   "idpUserID",
					IDPUserName: "username",
					IDPAccessToken: &crypto.CryptoValue{
						CryptoType: crypto.TypeEncryption,
						Algorithm:  "enc",
						KeyID:      "id",
						Crypted:    []byte("accessToken"),
					},
					IDPIDToken: "idToken",
					UserID:     "userID",
					State:      domain.IDPIntentStateSucceeded,
				},
				alg: decryption(nil),
			},
			res{
				resp: &user.RetrieveIdentityProviderInformationResponse{
					Details: &object_pb.Details{
						Sequence:      123,
						ChangeDate:    timestamppb.New(time.Date(2019, 4, 1, 1, 1, 1, 1, time.Local)),
						ResourceOwner: "ro",
					},
					IdpInformation: &user.IDPInformation{
						Access: &user.IDPInformation_Oauth{
							Oauth: &user.IDPOAuthAccessInformation{
								AccessToken: "accessToken",
								IdToken:     gu.Ptr("idToken"),
							},
						},
						IdpId:    "idpID",
						UserId:   "idpUserID",
						UserName: "username",
						RawInformation: func() *structpb.Struct {
							s, err := structpb.NewStruct(map[string]interface{}{
								"userID":   "idpUserID",
								"username": "username",
							})
							require.NoError(t, err)
							return s
						}(),
					},
				},
				err: nil,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := intentToIDPInformationPb(tt.args.intent, tt.args.alg)
			require.ErrorIs(t, err, tt.res.err)
			grpc.AllFieldsEqual(t, got.ProtoReflect(), tt.res.resp.ProtoReflect(), grpc.CustomMappers)
			if tt.res.resp != nil {
				grpc.AllFieldsSet(t, got.ProtoReflect(), ignoreTypes...)
			}
		})
	}
}

func Test_authMethodTypesToPb(t *testing.T) {
	tests := []struct {
		name        string
		methodTypes []domain.UserAuthMethodType
		want        []user.AuthenticationMethodType
	}{
		{
			"empty list",
			nil,
			[]user.AuthenticationMethodType{},
		},
		{
			"list",
			[]domain.UserAuthMethodType{
				domain.UserAuthMethodTypePasswordless,
			},
			[]user.AuthenticationMethodType{
				user.AuthenticationMethodType_AUTHENTICATION_METHOD_TYPE_PASSKEY,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, authMethodTypesToPb(tt.methodTypes), "authMethodTypesToPb(%v)", tt.methodTypes)
		})
	}
}

func Test_authMethodTypeToPb(t *testing.T) {
	tests := []struct {
		name       string
		methodType domain.UserAuthMethodType
		want       user.AuthenticationMethodType
	}{
		{
			"uspecified",
			domain.UserAuthMethodTypeUnspecified,
			user.AuthenticationMethodType_AUTHENTICATION_METHOD_TYPE_UNSPECIFIED,
		},
		{
			"(t)otp",
			domain.UserAuthMethodTypeOTP,
			user.AuthenticationMethodType_AUTHENTICATION_METHOD_TYPE_TOTP,
		},
		{
			"u2f",
			domain.UserAuthMethodTypeU2F,
			user.AuthenticationMethodType_AUTHENTICATION_METHOD_TYPE_U2F,
		},
		{
			"passkey",
			domain.UserAuthMethodTypePasswordless,
			user.AuthenticationMethodType_AUTHENTICATION_METHOD_TYPE_PASSKEY,
		},
		{
			"password",
			domain.UserAuthMethodTypePassword,
			user.AuthenticationMethodType_AUTHENTICATION_METHOD_TYPE_PASSWORD,
		},
		{
			"idp",
			domain.UserAuthMethodTypeIDP,
			user.AuthenticationMethodType_AUTHENTICATION_METHOD_TYPE_IDP,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, authMethodTypeToPb(tt.methodType), "authMethodTypeToPb(%v)", tt.methodType)
		})
	}
}
