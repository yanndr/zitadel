package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zitadel/zitadel/internal/domain"
	caos_errs "github.com/zitadel/zitadel/internal/errors"
	"github.com/zitadel/zitadel/internal/query"
)

func TestNotify_SendEmailVerificationCode(t *testing.T) {
	type args struct {
		user    *query.NotifyUser
		origin  string
		code    string
		urlTmpl string
	}
	tests := []struct {
		name    string
		args    args
		want    *notifyResult
		wantErr error
	}{
		{
			name: "default URL",
			args: args{
				user: &query.NotifyUser{
					ID:            "user1",
					ResourceOwner: "org1",
				},
				origin:  "https://example.com",
				code:    "123",
				urlTmpl: "",
			},
			want: &notifyResult{
				url:                                "https://example.com/ui/login/mail/verification?userID=user1&code=123&orgID=org1",
				args:                               map[string]interface{}{"Code": "123"},
				messageType:                        domain.VerifyEmailMessageType,
				allowUnverifiedNotificationChannel: true,
			},
		},
		{
			name: "template error",
			args: args{
				user: &query.NotifyUser{
					ID:            "user1",
					ResourceOwner: "org1",
				},
				origin:  "https://example.com",
				code:    "123",
				urlTmpl: "{{",
			},
			want:    &notifyResult{},
			wantErr: caos_errs.ThrowInvalidArgument(nil, "DOMAIN-oGh5e", "Errors.User.InvalidURLTemplate"),
		},
		{
			name: "template success",
			args: args{
				user: &query.NotifyUser{
					ID:            "user1",
					ResourceOwner: "org1",
				},
				origin:  "https://example.com",
				code:    "123",
				urlTmpl: "https://example.com/email/verify?userID={{.UserID}}&code={{.Code}}&orgID={{.OrgID}}",
			},
			want: &notifyResult{
				url:                                "https://example.com/email/verify?userID=user1&code=123&orgID=org1",
				args:                               map[string]interface{}{"Code": "123"},
				messageType:                        domain.VerifyEmailMessageType,
				allowUnverifiedNotificationChannel: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, notify := mockNotify()
			err := notify.SendEmailVerificationCode(tt.args.user, tt.args.origin, tt.args.code, tt.args.urlTmpl)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.want, got)
		})
	}
}
