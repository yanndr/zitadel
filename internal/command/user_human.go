package command

import (
	"context"
	"strings"

	"golang.org/x/text/language"

	"github.com/zitadel/zitadel/internal/command/preparation"
	"github.com/zitadel/zitadel/internal/crypto"
	"github.com/zitadel/zitadel/internal/domain"
	"github.com/zitadel/zitadel/internal/errors"
	"github.com/zitadel/zitadel/internal/eventstore"
	"github.com/zitadel/zitadel/internal/eventstore/v1/models"
	"github.com/zitadel/zitadel/internal/repository/user"
)

func (c *Commands) getHuman(ctx context.Context, userID, resourceowner string) (*domain.Human, error) {
	human, err := c.getHumanWriteModelByID(ctx, userID, resourceowner)
	if err != nil {
		return nil, err
	}
	if !isUserStateExists(human.UserState) {
		return nil, errors.ThrowNotFound(nil, "COMMAND-M9dsd", "Errors.User.NotFound")
	}
	return writeModelToHuman(human), nil
}

type AddHuman struct {
	// ID is optional, if empty it will be generated
	ID string
	// Username is required
	Username string
	// FirstName is required
	FirstName string
	// LastName is required
	LastName string
	// NickName is required
	NickName string
	// DisplayName is required
	DisplayName string
	// Email is required
	Email Email
	// PreferredLanguage is required
	PreferredLanguage language.Tag
	// Gender is required
	Gender domain.Gender
	// Phone represents an international phone number
	Phone Phone
	// Password is optional
	Password string
	// BcryptedPassword is optional
	BcryptedPassword string
	// PasswordChangeRequired is used if the `Password`-field is set
	PasswordChangeRequired bool
	Passwordless           bool
	ExternalIDP            bool
	Register               bool
	Metadata               []*AddMetadataEntry

	// Links are optional
	Links []*AddLink

	// Details are set after a successful execution of the command
	Details *domain.ObjectDetails

	// EmailCode is set by the command
	EmailCode *string
}

type AddLink struct {
	IDPID         string
	DisplayName   string
	IDPExternalID string
}

func (h *AddHuman) Validate() (err error) {
	if err := h.Email.Validate(); err != nil {
		return err
	}
	if h.Username = strings.TrimSpace(h.Username); h.Username == "" {
		return errors.ThrowInvalidArgument(nil, "V2-zzad3", "Errors.Invalid.Argument")
	}

	if h.FirstName = strings.TrimSpace(h.FirstName); h.FirstName == "" {
		return errors.ThrowInvalidArgument(nil, "USER-UCej2", "Errors.User.Profile.FirstNameEmpty")
	}
	if h.LastName = strings.TrimSpace(h.LastName); h.LastName == "" {
		return errors.ThrowInvalidArgument(nil, "USER-4hB7d", "Errors.User.Profile.LastNameEmpty")
	}
	h.ensureDisplayName()

	if h.Phone.Number != "" {
		if h.Phone.Number, err = h.Phone.Number.Normalize(); err != nil {
			return err
		}
	}

	for _, metadataEntry := range h.Metadata {
		if err := metadataEntry.Valid(); err != nil {
			return err
		}
	}
	return nil
}

type AddMetadataEntry struct {
	Key   string
	Value []byte
}

func (m *AddMetadataEntry) Valid() error {
	if m.Key = strings.TrimSpace(m.Key); m.Key == "" {
		return errors.ThrowInvalidArgument(nil, "USER-Drght", "Errors.User.Metadata.KeyEmpty")
	}
	if len(m.Value) == 0 {
		return errors.ThrowInvalidArgument(nil, "USER-Dbgth", "Errors.User.Metadata.ValueEmpty")
	}
	return nil
}

func (c *Commands) AddHuman(ctx context.Context, resourceOwner string, human *AddHuman, allowInitMail bool) (err error) {
	if resourceOwner == "" {
		return errors.ThrowInvalidArgument(nil, "COMMA-5Ky74", "Errors.Internal")
	}
	cmds, err := preparation.PrepareCommands(ctx, c.eventstore.Filter,
		c.AddHumanCommand(
			human,
			resourceOwner,
			c.userPasswordAlg,
			c.userEncryption,
			allowInitMail,
		))
	if err != nil {
		return err
	}

	events, err := c.eventstore.Push(ctx, cmds...)
	if err != nil {
		return err
	}
	human.Details = &domain.ObjectDetails{
		Sequence:      events[len(events)-1].Sequence(),
		EventDate:     events[len(events)-1].CreationDate(),
		ResourceOwner: events[len(events)-1].Aggregate().ResourceOwner,
	}

	return nil
}

type humanCreationCommand interface {
	eventstore.Command
	AddPhoneData(phoneNumber domain.PhoneNumber)
	AddPasswordData(secret *crypto.CryptoValue, changeRequired bool)
}

func (c *Commands) AddHumanCommand(human *AddHuman, orgID string, passwordAlg crypto.HashAlgorithm, codeAlg crypto.EncryptionAlgorithm, allowInitMail bool) preparation.Validation {
	return func() (_ preparation.CreateCommands, err error) {
		if err := human.Validate(); err != nil {
			return nil, err
		}

		return func(ctx context.Context, filter preparation.FilterToQueryReducer) ([]eventstore.Command, error) {
			if err := c.addHumanCommandCheckID(ctx, filter, human, orgID); err != nil {
				return nil, err
			}
			a := user.NewAggregate(human.ID, orgID)

			domainPolicy, err := domainPolicyWriteModel(ctx, filter, a.ResourceOwner)
			if err != nil {
				return nil, err
			}

			if err = userValidateDomain(ctx, a, human.Username, domainPolicy.UserLoginMustBeDomain, filter); err != nil {
				return nil, err
			}

			var createCmd humanCreationCommand
			if human.Register {
				createCmd = user.NewHumanRegisteredEvent(
					ctx,
					&a.Aggregate,
					human.Username,
					human.FirstName,
					human.LastName,
					human.NickName,
					human.DisplayName,
					human.PreferredLanguage,
					human.Gender,
					human.Email.Address,
					domainPolicy.UserLoginMustBeDomain,
				)
			} else {
				createCmd = user.NewHumanAddedEvent(
					ctx,
					&a.Aggregate,
					human.Username,
					human.FirstName,
					human.LastName,
					human.NickName,
					human.DisplayName,
					human.PreferredLanguage,
					human.Gender,
					human.Email.Address,
					domainPolicy.UserLoginMustBeDomain,
				)
			}

			if human.Phone.Number != "" {
				createCmd.AddPhoneData(human.Phone.Number)
			}

			if err := addHumanCommandPassword(ctx, filter, createCmd, human, passwordAlg); err != nil {
				return nil, err
			}

			cmds := make([]eventstore.Command, 0, 3)
			cmds = append(cmds, createCmd)

			cmds, err = c.addHumanCommandEmail(ctx, filter, cmds, a, human, codeAlg, allowInitMail)
			if err != nil {
				return nil, err
			}

			cmds, err = c.addHumanCommandPhone(ctx, filter, cmds, a, human, codeAlg)
			if err != nil {
				return nil, err
			}

			for _, metadataEntry := range human.Metadata {
				cmds = append(cmds, user.NewMetadataSetEvent(
					ctx,
					&a.Aggregate,
					metadataEntry.Key,
					metadataEntry.Value,
				))
			}
			for _, link := range human.Links {
				cmd, err := addLink(ctx, filter, a, link)
				if err != nil {
					return nil, err
				}
				cmds = append(cmds, cmd)
			}

			return cmds, nil
		}, nil
	}
}

func (c *Commands) addHumanCommandEmail(ctx context.Context, filter preparation.FilterToQueryReducer, cmds []eventstore.Command, a *user.Aggregate, human *AddHuman, codeAlg crypto.EncryptionAlgorithm, allowInitMail bool) ([]eventstore.Command, error) {
	if human.Email.Verified {
		cmds = append(cmds, user.NewHumanEmailVerifiedEvent(ctx, &a.Aggregate))
	}

	// if allowInitMail, used for v1 api (system, admin, mgmt, auth):
	// add init code if
	// email not verified or
	// user not registered and password set
	if allowInitMail && human.shouldAddInitCode() {
		initCode, err := newUserInitCode(ctx, filter, codeAlg)
		if err != nil {
			return nil, err
		}
		return append(cmds, user.NewHumanInitialCodeAddedEvent(ctx, &a.Aggregate, initCode.Crypted, initCode.Expiry)), nil
	}
	if !human.Email.Verified {
		emailCode, err := c.newEmailCode(ctx, filter, codeAlg)
		if err != nil {
			return nil, err
		}
		if human.Email.ReturnCode {
			human.EmailCode = &emailCode.Plain
		}
		return append(cmds, user.NewHumanEmailCodeAddedEventV2(ctx, &a.Aggregate, emailCode.Crypted, emailCode.Expiry, human.Email.URLTemplate, human.Email.ReturnCode)), nil
	}
	return cmds, nil
}

func addLink(ctx context.Context, filter preparation.FilterToQueryReducer, a *user.Aggregate, link *AddLink) (eventstore.Command, error) {
	exists, err := ExistsIDP(ctx, filter, link.IDPID, a.ResourceOwner)
	if !exists || err != nil {
		return nil, errors.ThrowPreconditionFailed(err, "COMMAND-39nf2", "Errors.IDPConfig.NotExisting")
	}
	return user.NewUserIDPLinkAddedEvent(ctx, &a.Aggregate, link.IDPID, link.DisplayName, link.IDPExternalID), nil
}

func (c *Commands) addHumanCommandPhone(ctx context.Context, filter preparation.FilterToQueryReducer, cmds []eventstore.Command, a *user.Aggregate, human *AddHuman, codeAlg crypto.EncryptionAlgorithm) ([]eventstore.Command, error) {
	if human.Phone.Number == "" {
		return cmds, nil
	}
	if human.Phone.Verified {
		return append(cmds, user.NewHumanPhoneVerifiedEvent(ctx, &a.Aggregate)), nil
	}
	phoneCode, err := newPhoneCode(ctx, filter, codeAlg)
	if err != nil {
		return nil, err
	}
	return append(cmds, user.NewHumanPhoneCodeAddedEvent(ctx, &a.Aggregate, phoneCode.Crypted, phoneCode.Expiry)), nil
}

func (c *Commands) addHumanCommandCheckID(ctx context.Context, filter preparation.FilterToQueryReducer, human *AddHuman, orgID string) (err error) {
	if human.ID == "" {
		human.ID, err = c.idGenerator.Next()
		if err != nil {
			return err
		}
	}
	existingHuman, err := humanWriteModelByID(ctx, filter, human.ID, orgID)
	if err != nil {
		return err
	}
	if isUserStateExists(existingHuman.UserState) {
		return errors.ThrowPreconditionFailed(nil, "COMMAND-k2unb", "Errors.User.AlreadyExisting")
	}
	return nil
}

func addHumanCommandPassword(ctx context.Context, filter preparation.FilterToQueryReducer, createCmd humanCreationCommand, human *AddHuman, passwordAlg crypto.HashAlgorithm) (err error) {
	if human.Password != "" {
		if err = humanValidatePassword(ctx, filter, human.Password); err != nil {
			return err
		}

		secret, err := crypto.Hash([]byte(human.Password), passwordAlg)
		if err != nil {
			return err
		}
		createCmd.AddPasswordData(secret, human.PasswordChangeRequired)
		return nil
	}

	if human.BcryptedPassword != "" {
		createCmd.AddPasswordData(crypto.FillHash([]byte(human.BcryptedPassword), passwordAlg), human.PasswordChangeRequired)
	}
	return nil
}

func userValidateDomain(ctx context.Context, a *user.Aggregate, username string, mustBeDomain bool, filter preparation.FilterToQueryReducer) error {
	if mustBeDomain {
		return nil
	}

	index := strings.LastIndex(username, "@")
	if index < 0 {
		return nil
	}

	domainCheck := NewOrgDomainVerifiedWriteModel(username[index+1:])
	events, err := filter(ctx, domainCheck.Query())
	if err != nil {
		return err
	}
	domainCheck.AppendEvents(events...)
	if err = domainCheck.Reduce(); err != nil {
		return err
	}

	if domainCheck.Verified && domainCheck.ResourceOwner != a.ResourceOwner {
		return errors.ThrowInvalidArgument(nil, "COMMAND-SFd21", "Errors.User.DomainNotAllowedAsUsername")
	}

	return nil
}

func humanValidatePassword(ctx context.Context, filter preparation.FilterToQueryReducer, password string) error {
	passwordComplexity, err := passwordComplexityPolicyWriteModel(ctx, filter)
	if err != nil {
		return err
	}

	return passwordComplexity.Validate(password)
}

func (h *AddHuman) ensureDisplayName() {
	if strings.TrimSpace(h.DisplayName) != "" {
		return
	}
	if strings.TrimSpace(h.FirstName) != "" && strings.TrimSpace(h.LastName) != "" {
		h.DisplayName = h.FirstName + " " + h.LastName
		return
	}
	if strings.TrimSpace(string(h.Email.Address)) != "" {
		h.DisplayName = string(h.Email.Address)
		return
	}
	h.DisplayName = h.Username
}

// shouldAddInitCode returns true for all added Humans which:
// - were not added from an external IDP
// - and either:
//   - have no verified email
//     and / or
//   - have no authentication method (password / passwordless)
func (h *AddHuman) shouldAddInitCode() bool {
	return !h.ExternalIDP &&
		!h.Email.Verified ||
		!h.Passwordless &&
			h.Password == ""
}

func (c *Commands) ImportHuman(ctx context.Context, orgID string, human *domain.Human, passwordless bool, links []*domain.UserIDPLink, initCodeGenerator, emailCodeGenerator, phoneCodeGenerator, passwordlessCodeGenerator crypto.Generator) (_ *domain.Human, passwordlessCode *domain.PasswordlessInitCode, err error) {
	if orgID == "" {
		return nil, nil, errors.ThrowInvalidArgument(nil, "COMMAND-5N8fs", "Errors.ResourceOwnerMissing")
	}
	domainPolicy, err := c.getOrgDomainPolicy(ctx, orgID)
	if err != nil {
		return nil, nil, errors.ThrowPreconditionFailed(err, "COMMAND-2N9fs", "Errors.Org.DomainPolicy.NotFound")
	}
	pwPolicy, err := c.getOrgPasswordComplexityPolicy(ctx, orgID)
	if err != nil {
		return nil, nil, errors.ThrowPreconditionFailed(err, "COMMAND-4N8gs", "Errors.Org.PasswordComplexityPolicy.NotFound")
	}

	if human.AggregateID != "" {
		existing, err := c.getHumanWriteModelByID(ctx, human.AggregateID, human.ResourceOwner)
		if err != nil {
			return nil, nil, err
		}

		if existing.UserState != domain.UserStateUnspecified {
			return nil, nil, errors.ThrowPreconditionFailed(nil, "COMMAND-ziuna", "Errors.User.AlreadyExisting")
		}
	}

	events, addedHuman, addedCode, code, err := c.importHuman(ctx, orgID, human, passwordless, links, domainPolicy, pwPolicy, initCodeGenerator, emailCodeGenerator, phoneCodeGenerator, passwordlessCodeGenerator)
	if err != nil {
		return nil, nil, err
	}
	pushedEvents, err := c.eventstore.Push(ctx, events...)
	if err != nil {
		return nil, nil, err
	}

	err = AppendAndReduce(addedHuman, pushedEvents...)
	if err != nil {
		return nil, nil, err
	}
	if addedCode != nil {
		err = AppendAndReduce(addedCode, pushedEvents...)
		if err != nil {
			return nil, nil, err
		}
		passwordlessCode = writeModelToPasswordlessInitCode(addedCode, code)
	}

	return writeModelToHuman(addedHuman), passwordlessCode, nil
}

func (c *Commands) RegisterHuman(ctx context.Context, orgID string, human *domain.Human, link *domain.UserIDPLink, orgMemberRoles []string, initCodeGenerator, emailCodeGenerator, phoneCodeGenerator crypto.Generator) (*domain.Human, error) {
	if orgID == "" {
		return nil, errors.ThrowInvalidArgument(nil, "COMMAND-GEdf2", "Errors.ResourceOwnerMissing")
	}
	domainPolicy, err := c.getOrgDomainPolicy(ctx, orgID)
	if err != nil {
		return nil, errors.ThrowPreconditionFailed(err, "COMMAND-33M9f", "Errors.Org.DomainPolicy.NotFound")
	}
	pwPolicy, err := c.getOrgPasswordComplexityPolicy(ctx, orgID)
	if err != nil {
		return nil, errors.ThrowPreconditionFailed(err, "COMMAND-M5Fsd", "Errors.Org.PasswordComplexityPolicy.NotFound")
	}
	loginPolicy, err := c.getOrgLoginPolicy(ctx, orgID)
	if err != nil {
		return nil, errors.ThrowPreconditionFailed(err, "COMMAND-Dfg3g", "Errors.Org.LoginPolicy.NotFound")
	}
	if !loginPolicy.AllowRegister {
		return nil, errors.ThrowPreconditionFailed(err, "COMMAND-SAbr3", "Errors.Org.LoginPolicy.RegistrationNotAllowed")
	}
	userEvents, registeredHuman, err := c.registerHuman(ctx, orgID, human, link, domainPolicy, pwPolicy, initCodeGenerator, emailCodeGenerator, phoneCodeGenerator)
	if err != nil {
		return nil, err
	}

	orgMemberWriteModel := NewOrgMemberWriteModel(orgID, registeredHuman.AggregateID)
	orgAgg := OrgAggregateFromWriteModel(&orgMemberWriteModel.WriteModel)
	if len(orgMemberRoles) > 0 {
		orgMember := &domain.Member{
			ObjectRoot: models.ObjectRoot{
				AggregateID: orgID,
			},
			UserID: human.AggregateID,
			Roles:  orgMemberRoles,
		}
		memberEvent, err := c.addOrgMember(ctx, orgAgg, orgMemberWriteModel, orgMember)
		if err != nil {
			return nil, err
		}
		userEvents = append(userEvents, memberEvent)
	}

	pushedEvents, err := c.eventstore.Push(ctx, userEvents...)
	if err != nil {
		return nil, err
	}

	err = AppendAndReduce(registeredHuman, pushedEvents...)
	if err != nil {
		return nil, err
	}
	return writeModelToHuman(registeredHuman), nil
}

func (c *Commands) importHuman(ctx context.Context, orgID string, human *domain.Human, passwordless bool, links []*domain.UserIDPLink, domainPolicy *domain.DomainPolicy, pwPolicy *domain.PasswordComplexityPolicy, initCodeGenerator, emailCodeGenerator, phoneCodeGenerator, passwordlessCodeGenerator crypto.Generator) (events []eventstore.Command, humanWriteModel *HumanWriteModel, passwordlessCodeWriteModel *HumanPasswordlessInitCodeWriteModel, code string, err error) {
	if orgID == "" {
		return nil, nil, nil, "", errors.ThrowInvalidArgument(nil, "COMMAND-00p2b", "Errors.Org.Empty")
	}
	if err := human.Normalize(); err != nil {
		return nil, nil, nil, "", err
	}
	events, humanWriteModel, err = c.createHuman(ctx, orgID, human, links, false, passwordless, domainPolicy, pwPolicy, initCodeGenerator, emailCodeGenerator, phoneCodeGenerator)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if passwordless {
		var codeEvent eventstore.Command
		codeEvent, passwordlessCodeWriteModel, code, err = c.humanAddPasswordlessInitCode(ctx, human.AggregateID, orgID, true, passwordlessCodeGenerator)
		if err != nil {
			return nil, nil, nil, "", err
		}
		events = append(events, codeEvent)
	}
	return events, humanWriteModel, passwordlessCodeWriteModel, code, nil
}

func (c *Commands) registerHuman(ctx context.Context, orgID string, human *domain.Human, link *domain.UserIDPLink, domainPolicy *domain.DomainPolicy, pwPolicy *domain.PasswordComplexityPolicy, initCodeGenerator, emailCodeGenerator, phoneCodeGenerator crypto.Generator) ([]eventstore.Command, *HumanWriteModel, error) {
	if human == nil {
		return nil, nil, errors.ThrowInvalidArgument(nil, "COMMAND-JKefw", "Errors.User.Invalid")
	}
	if human.Username = strings.TrimSpace(human.Username); human.Username == "" {
		human.Username = string(human.EmailAddress)
	}
	if orgID == "" {
		return nil, nil, errors.ThrowInvalidArgument(nil, "COMMAND-hYsVH", "Errors.Org.Empty")
	}
	if err := human.Normalize(); err != nil {
		return nil, nil, err
	}
	if link == nil && (human.Password == nil || human.Password.SecretString == "") {
		return nil, nil, errors.ThrowInvalidArgument(nil, "COMMAND-X23na", "Errors.User.Password.Empty")
	}
	if human.Password != nil && human.Password.SecretString != "" {
		human.Password.ChangeRequired = false
	}
	var links []*domain.UserIDPLink
	if link != nil {
		links = append(links, link)
	}
	return c.createHuman(ctx, orgID, human, links, true, false, domainPolicy, pwPolicy, initCodeGenerator, emailCodeGenerator, phoneCodeGenerator)
}

func (c *Commands) createHuman(ctx context.Context, orgID string, human *domain.Human, links []*domain.UserIDPLink, selfregister, passwordless bool, domainPolicy *domain.DomainPolicy, pwPolicy *domain.PasswordComplexityPolicy, initCodeGenerator, emailCodeGenerator, phoneCodeGenerator crypto.Generator) (events []eventstore.Command, addedHuman *HumanWriteModel, err error) {
	if err := human.CheckDomainPolicy(domainPolicy); err != nil {
		return nil, nil, err
	}
	human.Username = strings.TrimSpace(human.Username)
	human.EmailAddress = human.EmailAddress.Normalize()
	if !domainPolicy.UserLoginMustBeDomain {
		index := strings.LastIndex(human.Username, "@")
		if index > 1 {
			domainCheck := NewOrgDomainVerifiedWriteModel(human.Username[index+1:])
			if err := c.eventstore.FilterToQueryReducer(ctx, domainCheck); err != nil {
				return nil, nil, err
			}
			if domainCheck.Verified && domainCheck.ResourceOwner != orgID {
				return nil, nil, errors.ThrowInvalidArgument(nil, "COMMAND-SFd21", "Errors.User.DomainNotAllowedAsUsername")
			}
		}
	}

	if human.AggregateID == "" {
		userID, err := c.idGenerator.Next()
		if err != nil {
			return nil, nil, err
		}
		human.AggregateID = userID
	}

	human.EnsureDisplayName()
	if human.Password != nil {
		if err := human.HashPasswordIfExisting(pwPolicy, c.userPasswordAlg, human.Password.ChangeRequired); err != nil {
			return nil, nil, err
		}
	}

	addedHuman = NewHumanWriteModel(human.AggregateID, orgID)
	//TODO: adlerhurst maybe we could simplify the code below
	userAgg := UserAggregateFromWriteModel(&addedHuman.WriteModel)

	if selfregister {
		events = append(events, createRegisterHumanEvent(ctx, userAgg, human, domainPolicy.UserLoginMustBeDomain))
	} else {
		events = append(events, createAddHumanEvent(ctx, userAgg, human, domainPolicy.UserLoginMustBeDomain))
	}

	for _, link := range links {
		event, err := c.addUserIDPLink(ctx, userAgg, link)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, event)
	}

	if human.IsInitialState(passwordless, len(links) > 0) {
		initCode, err := domain.NewInitUserCode(initCodeGenerator)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, user.NewHumanInitialCodeAddedEvent(ctx, userAgg, initCode.Code, initCode.Expiry))
	} else {
		if human.Email != nil && human.EmailAddress != "" && human.IsEmailVerified {
			events = append(events, user.NewHumanEmailVerifiedEvent(ctx, userAgg))
		} else {
			emailCode, _, err := domain.NewEmailCode(emailCodeGenerator)
			if err != nil {
				return nil, nil, err
			}
			events = append(events, user.NewHumanEmailCodeAddedEvent(ctx, userAgg, emailCode.Code, emailCode.Expiry))
		}
	}

	if human.Phone != nil && human.PhoneNumber != "" && !human.IsPhoneVerified {
		phoneCode, err := domain.NewPhoneCode(phoneCodeGenerator)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, user.NewHumanPhoneCodeAddedEvent(ctx, userAgg, phoneCode.Code, phoneCode.Expiry))
	} else if human.Phone != nil && human.PhoneNumber != "" && human.IsPhoneVerified {
		events = append(events, user.NewHumanPhoneVerifiedEvent(ctx, userAgg))
	}

	return events, addedHuman, nil
}

func (c *Commands) HumanSkipMFAInit(ctx context.Context, userID, resourceowner string) (err error) {
	if userID == "" {
		return errors.ThrowInvalidArgument(nil, "COMMAND-2xpX9", "Errors.User.UserIDMissing")
	}

	existingHuman, err := c.getHumanWriteModelByID(ctx, userID, resourceowner)
	if err != nil {
		return err
	}
	if !isUserStateExists(existingHuman.UserState) {
		return errors.ThrowNotFound(nil, "COMMAND-m9cV8", "Errors.User.NotFound")
	}

	_, err = c.eventstore.Push(ctx,
		user.NewHumanMFAInitSkippedEvent(ctx, UserAggregateFromWriteModel(&existingHuman.WriteModel)))
	return err
}

// TODO: adlerhurst maybe we can simplify createAddHumanEvent and createRegisterHumanEvent
func createAddHumanEvent(ctx context.Context, aggregate *eventstore.Aggregate, human *domain.Human, userLoginMustBeDomain bool) *user.HumanAddedEvent {
	addEvent := user.NewHumanAddedEvent(
		ctx,
		aggregate,
		human.Username,
		human.FirstName,
		human.LastName,
		human.NickName,
		human.DisplayName,
		human.PreferredLanguage,
		human.Gender,
		human.EmailAddress,
		userLoginMustBeDomain,
	)
	if human.Phone != nil {
		addEvent.AddPhoneData(human.PhoneNumber)
	}
	if human.Address != nil {
		addEvent.AddAddressData(
			human.Country,
			human.Locality,
			human.PostalCode,
			human.Region,
			human.StreetAddress)
	}
	if human.Password != nil {
		addEvent.AddPasswordData(human.Password.SecretCrypto, human.Password.ChangeRequired)
	}
	if human.HashedPassword != nil {
		addEvent.AddPasswordData(human.HashedPassword.SecretCrypto, false)
	}
	return addEvent
}

func createRegisterHumanEvent(ctx context.Context, aggregate *eventstore.Aggregate, human *domain.Human, userLoginMustBeDomain bool) *user.HumanRegisteredEvent {
	addEvent := user.NewHumanRegisteredEvent(
		ctx,
		aggregate,
		human.Username,
		human.FirstName,
		human.LastName,
		human.NickName,
		human.DisplayName,
		human.PreferredLanguage,
		human.Gender,
		human.EmailAddress,
		userLoginMustBeDomain,
	)
	if human.Phone != nil {
		addEvent.AddPhoneData(human.PhoneNumber)
	}
	if human.Address != nil {
		addEvent.AddAddressData(
			human.Country,
			human.Locality,
			human.PostalCode,
			human.Region,
			human.StreetAddress)
	}
	if human.Password != nil {
		addEvent.AddPasswordData(human.Password.SecretCrypto, human.Password.ChangeRequired)
	}
	if human.HashedPassword != nil {
		addEvent.AddPasswordData(human.HashedPassword.SecretCrypto, false)
	}
	return addEvent
}

func (c *Commands) HumansSignOut(ctx context.Context, agentID string, userIDs []string) error {
	if agentID == "" {
		return errors.ThrowInvalidArgument(nil, "COMMAND-2M0ds", "Errors.User.UserIDMissing")
	}
	if len(userIDs) == 0 {
		return errors.ThrowInvalidArgument(nil, "COMMAND-M0od3", "Errors.User.UserIDMissing")
	}
	events := make([]eventstore.Command, 0)
	for _, userID := range userIDs {
		existingUser, err := c.getHumanWriteModelByID(ctx, userID, "")
		if err != nil {
			return err
		}
		if !isUserStateExists(existingUser.UserState) {
			continue
		}
		events = append(events, user.NewHumanSignedOutEvent(
			ctx,
			UserAggregateFromWriteModel(&existingUser.WriteModel),
			agentID))
	}
	if len(events) == 0 {
		return nil
	}
	_, err := c.eventstore.Push(ctx, events...)
	return err
}

func (c *Commands) getHumanWriteModelByID(ctx context.Context, userID, resourceowner string) (*HumanWriteModel, error) {
	humanWriteModel := NewHumanWriteModel(userID, resourceowner)
	err := c.eventstore.FilterToQueryReducer(ctx, humanWriteModel)
	if err != nil {
		return nil, err
	}
	return humanWriteModel, nil
}

func humanWriteModelByID(ctx context.Context, filter preparation.FilterToQueryReducer, userID, resourceowner string) (*HumanWriteModel, error) {
	humanWriteModel := NewHumanWriteModel(userID, resourceowner)
	events, err := filter(ctx, humanWriteModel.Query())
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return humanWriteModel, nil
	}
	humanWriteModel.AppendEvents(events...)
	err = humanWriteModel.Reduce()
	return humanWriteModel, err
}
