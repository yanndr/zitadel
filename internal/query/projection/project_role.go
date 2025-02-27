package projection

import (
	"context"

	"github.com/zitadel/zitadel/internal/errors"
	"github.com/zitadel/zitadel/internal/eventstore"
	"github.com/zitadel/zitadel/internal/eventstore/handler"
	"github.com/zitadel/zitadel/internal/eventstore/handler/crdb"
	"github.com/zitadel/zitadel/internal/repository/instance"
	"github.com/zitadel/zitadel/internal/repository/org"
	"github.com/zitadel/zitadel/internal/repository/project"
)

const (
	ProjectRoleProjectionTable = "projections.project_roles3"

	ProjectRoleColumnProjectID     = "project_id"
	ProjectRoleColumnKey           = "role_key"
	ProjectRoleColumnCreationDate  = "creation_date"
	ProjectRoleColumnChangeDate    = "change_date"
	ProjectRoleColumnSequence      = "sequence"
	ProjectRoleColumnResourceOwner = "resource_owner"
	ProjectRoleColumnInstanceID    = "instance_id"
	ProjectRoleColumnDisplayName   = "display_name"
	ProjectRoleColumnGroupName     = "group_name"
	ProjectRoleColumnOwnerRemoved  = "owner_removed"
)

type projectRoleProjection struct {
	crdb.StatementHandler
}

func newProjectRoleProjection(ctx context.Context, config crdb.StatementHandlerConfig) *projectRoleProjection {
	p := new(projectRoleProjection)
	config.ProjectionName = ProjectRoleProjectionTable
	config.Reducers = p.reducers()
	config.InitCheck = crdb.NewTableCheck(
		crdb.NewTable([]*crdb.Column{
			crdb.NewColumn(ProjectRoleColumnProjectID, crdb.ColumnTypeText),
			crdb.NewColumn(ProjectRoleColumnKey, crdb.ColumnTypeText),
			crdb.NewColumn(ProjectRoleColumnCreationDate, crdb.ColumnTypeTimestamp),
			crdb.NewColumn(ProjectRoleColumnChangeDate, crdb.ColumnTypeTimestamp),
			crdb.NewColumn(ProjectRoleColumnSequence, crdb.ColumnTypeInt64),
			crdb.NewColumn(ProjectRoleColumnResourceOwner, crdb.ColumnTypeText),
			crdb.NewColumn(ProjectRoleColumnInstanceID, crdb.ColumnTypeText),
			crdb.NewColumn(ProjectRoleColumnDisplayName, crdb.ColumnTypeText),
			crdb.NewColumn(ProjectRoleColumnGroupName, crdb.ColumnTypeText),
			crdb.NewColumn(ProjectRoleColumnOwnerRemoved, crdb.ColumnTypeBool, crdb.Default(false)),
		},
			crdb.NewPrimaryKey(ProjectRoleColumnInstanceID, ProjectRoleColumnProjectID, ProjectRoleColumnKey),
			crdb.WithIndex(crdb.NewIndex("owner_removed", []string{ProjectRoleColumnOwnerRemoved})),
		),
	)
	p.StatementHandler = crdb.NewStatementHandler(ctx, config)
	return p
}

func (p *projectRoleProjection) reducers() []handler.AggregateReducer {
	return []handler.AggregateReducer{
		{
			Aggregate: project.AggregateType,
			EventRedusers: []handler.EventReducer{
				{
					Event:  project.RoleAddedType,
					Reduce: p.reduceProjectRoleAdded,
				},
				{
					Event:  project.RoleChangedType,
					Reduce: p.reduceProjectRoleChanged,
				},
				{
					Event:  project.RoleRemovedType,
					Reduce: p.reduceProjectRoleRemoved,
				},
				{
					Event:  project.ProjectRemovedType,
					Reduce: p.reduceProjectRemoved,
				},
			},
		},
		{
			Aggregate: org.AggregateType,
			EventRedusers: []handler.EventReducer{
				{
					Event:  org.OrgRemovedEventType,
					Reduce: p.reduceOwnerRemoved,
				},
			},
		},
		{
			Aggregate: instance.AggregateType,
			EventRedusers: []handler.EventReducer{
				{
					Event:  instance.InstanceRemovedEventType,
					Reduce: reduceInstanceRemovedHelper(ProjectRoleColumnInstanceID),
				},
			},
		},
	}
}

func (p *projectRoleProjection) reduceProjectRoleAdded(event eventstore.Event) (*handler.Statement, error) {
	e, ok := event.(*project.RoleAddedEvent)
	if !ok {
		return nil, errors.ThrowInvalidArgumentf(nil, "HANDL-g92Fg", "reduce.wrong.event.type %s", project.RoleAddedType)
	}
	return crdb.NewCreateStatement(
		e,
		[]handler.Column{
			handler.NewCol(ProjectRoleColumnKey, e.Key),
			handler.NewCol(ProjectRoleColumnProjectID, e.Aggregate().ID),
			handler.NewCol(ProjectRoleColumnCreationDate, e.CreationDate()),
			handler.NewCol(ProjectRoleColumnChangeDate, e.CreationDate()),
			handler.NewCol(ProjectRoleColumnResourceOwner, e.Aggregate().ResourceOwner),
			handler.NewCol(ProjectRoleColumnInstanceID, e.Aggregate().InstanceID),
			handler.NewCol(ProjectRoleColumnSequence, e.Sequence()),
			handler.NewCol(ProjectRoleColumnDisplayName, e.DisplayName),
			handler.NewCol(ProjectRoleColumnGroupName, e.Group),
		},
	), nil
}

func (p *projectRoleProjection) reduceProjectRoleChanged(event eventstore.Event) (*handler.Statement, error) {
	e, ok := event.(*project.RoleChangedEvent)
	if !ok {
		return nil, errors.ThrowInvalidArgumentf(nil, "HANDL-sM0f", "reduce.wrong.event.type %s", project.GrantChangedType)
	}
	if e.DisplayName == nil && e.Group == nil {
		return crdb.NewNoOpStatement(e), nil
	}
	columns := make([]handler.Column, 0, 7)
	columns = append(columns, handler.NewCol(ProjectRoleColumnChangeDate, e.CreationDate()),
		handler.NewCol(ProjectRoleColumnSequence, e.Sequence()))
	if e.DisplayName != nil {
		columns = append(columns, handler.NewCol(ProjectRoleColumnDisplayName, *e.DisplayName))
	}
	if e.Group != nil {
		columns = append(columns, handler.NewCol(ProjectRoleColumnGroupName, *e.Group))
	}
	return crdb.NewUpdateStatement(
		e,
		columns,
		[]handler.Condition{
			handler.NewCond(ProjectRoleColumnKey, e.Key),
			handler.NewCond(ProjectRoleColumnProjectID, e.Aggregate().ID),
			handler.NewCond(ProjectRoleColumnInstanceID, e.Aggregate().InstanceID),
		},
	), nil
}

func (p *projectRoleProjection) reduceProjectRoleRemoved(event eventstore.Event) (*handler.Statement, error) {
	e, ok := event.(*project.RoleRemovedEvent)
	if !ok {
		return nil, errors.ThrowInvalidArgumentf(nil, "HANDL-L0fJf", "reduce.wrong.event.type %s", project.GrantRemovedType)
	}
	return crdb.NewDeleteStatement(
		e,
		[]handler.Condition{
			handler.NewCond(ProjectRoleColumnKey, e.Key),
			handler.NewCond(ProjectRoleColumnProjectID, e.Aggregate().ID),
			handler.NewCond(ProjectRoleColumnInstanceID, e.Aggregate().InstanceID),
		},
	), nil
}

func (p *projectRoleProjection) reduceProjectRemoved(event eventstore.Event) (*handler.Statement, error) {
	e, ok := event.(*project.ProjectRemovedEvent)
	if !ok {
		return nil, errors.ThrowInvalidArgumentf(nil, "HANDL-l0geG", "reduce.wrong.event.type %s", project.ProjectRemovedType)
	}
	return crdb.NewDeleteStatement(
		e,
		[]handler.Condition{
			handler.NewCond(ProjectRoleColumnProjectID, e.Aggregate().ID),
			handler.NewCond(ProjectRoleColumnInstanceID, e.Aggregate().InstanceID),
		},
	), nil
}

func (p *projectRoleProjection) reduceOwnerRemoved(event eventstore.Event) (*handler.Statement, error) {
	e, ok := event.(*org.OrgRemovedEvent)
	if !ok {
		return nil, errors.ThrowInvalidArgumentf(nil, "PROJE-3XrHY", "reduce.wrong.event.type %s", org.OrgRemovedEventType)
	}

	return crdb.NewUpdateStatement(
		e,
		[]handler.Column{
			handler.NewCol(ProjectRoleColumnChangeDate, e.CreationDate()),
			handler.NewCol(ProjectRoleColumnSequence, e.Sequence()),
			handler.NewCol(ProjectRoleColumnOwnerRemoved, true),
		},
		[]handler.Condition{
			handler.NewCond(ProjectRoleColumnInstanceID, e.Aggregate().InstanceID),
			handler.NewCond(ProjectRoleColumnResourceOwner, e.Aggregate().ID),
		},
	), nil
}
