package httpapi

import (
	"context"
	"io"

	"github.com/NickWilde18/Park/internal/docs"
	"github.com/NickWilde18/Park/internal/fail"
	"github.com/NickWilde18/Park/internal/grouping"
	"github.com/NickWilde18/Park/internal/identity"
)

var errNotAnAuthor = fail.New(fail.Forbidden, "not_an_author",
	"Only a member who wrote one of the group's pull requests, or an admin, may change its description.")

// PutGroupDoc stores a group's description from its zip, which replaces the one the group had (G17).
func (s *server) PutGroupDoc(ctx context.Context, req PutGroupDocRequestObject) (PutGroupDocResponseObject, error) {
	c, g, err := s.describer(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(req.Body, docs.MaxZip+1))
	if err != nil {
		return nil, err
	}
	files, err := docs.Read(data)
	if err != nil {
		return nil, err
	}
	if _, err := s.docs.Put(ctx, g.ID, c.Member.ID, files); err != nil {
		return nil, err
	}
	out, err := s.docsOf(ctx, []int64{g.ID})
	if err != nil {
		return nil, err
	}
	return PutGroupDoc200JSONResponse(out[g.ID]), nil
}

// DeleteGroupDoc removes a group's description; its link stops working.
func (s *server) DeleteGroupDoc(ctx context.Context, req DeleteGroupDocRequestObject) (DeleteGroupDocResponseObject, error) {
	_, g, err := s.describer(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := s.docs.Delete(ctx, g.ID); err != nil {
		return nil, err
	}
	return DeleteGroupDoc204Response{}, nil
}

// describer is the caller and the group, a merged group's ID leading to the group it joined, when the caller may
// change the group's description.
func (s *server) describer(ctx context.Context, id int64) (*identity.Caller, grouping.GroupView, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, grouping.GroupView{}, err
	}
	g, err := s.grouping.Group(ctx, id)
	if err != nil {
		return nil, grouping.GroupView{}, err
	}
	ok, err := s.mayDescribe(ctx, c, g)
	if err != nil {
		return nil, grouping.GroupView{}, err
	}
	if !ok {
		return nil, grouping.GroupView{}, errNotAnAuthor
	}
	return c, g, nil
}

// mayDescribe reports whether a caller may change a group's description (G17): an admin may, and so may a member one
// of whose linked accounts wrote one of the group's pull requests, open or closed.
func (s *server) mayDescribe(ctx context.Context, c *identity.Caller, g grouping.GroupView) (bool, error) {
	if c.Member.Admin {
		return true, nil
	}
	ids, err := s.id.Identities(ctx, c.Member.ID)
	if err != nil {
		return false, err
	}
	mine := map[string]bool{}
	for _, i := range ids {
		mine[accountKey(i.ForgeID, i.AccountID)] = true
	}
	for _, m := range g.Members {
		p, err := s.facts.PR(ctx, m.PR)
		if err != nil {
			return false, err
		}
		if mine[accountKey(p.Repo.ForgeID, p.State.Author)] {
			return true, nil
		}
	}
	return false, nil
}

// described are groups' descriptions, as the contract has them, by group.
type described map[int64]GroupDoc

// with gives a group its description, if it has one.
func (d described) with(g Group) Group {
	if doc, ok := d[g.Id]; ok {
		g.Doc = &doc
	}
	return g
}

// docsOf is the descriptions of groups, by group.
func (s *server) docsOf(ctx context.Context, groups []int64) (described, error) {
	out := described{}
	if s.docs == nil || len(groups) == 0 {
		return out, nil
	}
	ds, err := s.docs.Of(ctx, groups)
	if err != nil {
		return nil, err
	}
	for g, d := range ds {
		out[g] = GroupDoc{Link: d.Link(), Files: d.Files, Size: d.Size, UploadedBy: d.UploaderName, UploadedAt: d.UploadedAt}
	}
	return out, nil
}
