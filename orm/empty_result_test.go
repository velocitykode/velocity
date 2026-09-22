package orm

import (
	"context"
	"encoding/json"
	"testing"
)

// Plural reads must return an empty, non-nil slice when no row matches so
// callers can range and encode the result without a nil guard. JSON
// encoding is asserted directly because a nil slice marshals to `null`
// while an empty slice marshals to `[]`, and that difference is what a
// frontend sees.

func assertEmptyJSONArray(t *testing.T, label string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: marshal failed: %v", label, err)
	}
	if string(b) != "[]" {
		t.Errorf("%s: JSON = %s, want []", label, b)
	}
}

func TestGet_EmptyResult_ReturnsNonNilSlice(t *testing.T) {
	setupConvenienceTests(t)

	users, err := Model[TestUser]{}.Where("name = ?", "nobody").Get(context.Background())
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if users == nil {
		t.Fatal("Get returned nil slice on empty result, want empty non-nil slice")
	}
	if len(users) != 0 {
		t.Fatalf("expected 0 users, got %d", len(users))
	}
	assertEmptyJSONArray(t, "Get", users)
}

func TestPluck_EmptyResult_ReturnsNonNilSlice(t *testing.T) {
	setupConvenienceTests(t)

	names, err := Model[TestUser]{}.Where("name = ?", "nobody").Pluck(context.Background(), "name")
	if err != nil {
		t.Fatalf("Pluck failed: %v", err)
	}
	if names == nil {
		t.Fatal("Pluck returned nil slice on empty result, want empty non-nil slice")
	}
	assertEmptyJSONArray(t, "Pluck", names)
}

func TestPaginate_EmptyResult_DataIsNonNilSlice(t *testing.T) {
	setupConvenienceTests(t)

	page, err := Model[TestUser]{}.Where("name = ?", "nobody").Paginate(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("Paginate failed: %v", err)
	}
	if page.Data() == nil {
		t.Fatal("Paginate returned nil Data on empty result, want empty non-nil slice")
	}
	assertEmptyJSONArray(t, "Paginate.Data", page.Data())
}

func TestRawQuery_Get_EmptyResult_ReturnsNonNilSlice(t *testing.T) {
	env, cleanup := setupRawQueryTestDB(t, "sqlite")
	defer cleanup()

	rq := NewRawQuery[RawQueryUser]("SELECT id, name, email FROM raw_query_users WHERE age > ?", 100)
	rq.driver = env.driver
	users, err := rq.Get(context.Background())
	if err != nil {
		t.Fatalf("RawQuery.Get failed: %v", err)
	}
	if users == nil {
		t.Fatal("RawQuery.Get returned nil slice on empty result, want empty non-nil slice")
	}
	assertEmptyJSONArray(t, "RawQuery.Get", users)
}

func TestLoadRelations_HasMany_NoChildren_IsEmptySlice(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	// Charlie has no posts; Alice and Bob do, so the relation query runs
	// and Charlie's slice must still be assigned as empty, not left nil.
	users, err := RelUser{}.With("Posts").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	var charlie *RelUser
	for i := range users {
		if users[i].Name == "Charlie" {
			charlie = &users[i]
		}
	}
	if charlie == nil {
		t.Fatal("Charlie not found")
	}
	if charlie.Posts == nil {
		t.Fatal("Charlie.Posts is nil, want empty non-nil slice")
	}
	assertEmptyJSONArray(t, "Charlie.Posts", charlie.Posts)
}

func TestLoadRelations_HasMany_PointerSlice_NoChildren_IsEmptySlice(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	users, err := RelUserPtrSlice{}.With("Posts").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	charlie := findByName(users, "Charlie")
	if charlie == nil {
		t.Fatal("Charlie not found")
	}
	if charlie.Posts == nil {
		t.Fatal("Charlie.Posts is nil, want empty non-nil slice")
	}
	assertEmptyJSONArray(t, "Charlie.Posts", charlie.Posts)
}

func TestLoadRelations_HasMany_OnlyChildlessParents_IsEmptySlice(t *testing.T) {
	cleanup := withRelationDB(t)
	defer cleanup()

	// Only Charlie is selected. He has a key, so the relation query runs
	// and returns zero rows; the field must still end up as an empty slice.
	users, err := RelUser{}.Where("name = ?", "Charlie").With("Posts").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].Posts == nil {
		t.Fatal("Posts is nil, want empty non-nil slice")
	}
	assertEmptyJSONArray(t, "Posts", users[0].Posts)
}

func TestLoadRelations_HasMany_NoParentKeys_IsEmptySlice(t *testing.T) {
	// No DB round trip: parents with zero-valued keys take the early
	// return in loadRelation, which must still assign empty slices.
	cleanup := withRelationDB(t)
	defer cleanup()

	models := []RelUser{{Name: "ghost"}}
	q := newQuery[RelUser]().With("Posts")
	if err := q.loadRelations(context.Background(), &models); err != nil {
		t.Fatalf("loadRelations failed: %v", err)
	}
	if models[0].Posts == nil {
		t.Fatal("Posts is nil after no-key early return, want empty non-nil slice")
	}
	assertEmptyJSONArray(t, "Posts", models[0].Posts)
}

// Many-to-many has three paths that used to leave the field nil: a parent
// with no pivot rows while others have some, a result set where no parent
// has any pivot row, and parents that carry no id at all.

func TestLoadM2M_NoMembers_IsEmptySlice(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()

	// Team 3 "Empty" has no pivot rows; teams 1 and 2 do, so the pivot
	// query runs and team 3 must still be assigned an empty slice.
	teams, err := Team{}.With("Members").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	var empty *Team
	for i := range teams {
		if teams[i].Name == "Empty" {
			empty = &teams[i]
		}
	}
	if empty == nil {
		t.Fatal("team Empty not found")
	}
	if empty.Members == nil {
		t.Fatal("Empty.Members is nil, want empty non-nil slice")
	}
	assertEmptyJSONArray(t, "Empty.Members", empty.Members)
}

func TestLoadM2M_PointerSlice_NoMembers_IsEmptySlice(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()

	teams, err := TeamPtrMembers{}.With("Members").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	var empty *TeamPtrMembers
	for i := range teams {
		if teams[i].Name == "Empty" {
			empty = &teams[i]
		}
	}
	if empty == nil {
		t.Fatal("team Empty not found")
	}
	if empty.Members == nil {
		t.Fatal("Empty.Members is nil, want empty non-nil slice")
	}
	assertEmptyJSONArray(t, "Empty.Members", empty.Members)
}

func TestLoadM2M_OnlyMemberlessParents_IsEmptySlice(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()

	// Only team 3 is selected, so the pivot query returns zero rows and
	// the loader takes its no-related-ids early return.
	teams, err := Team{}.Where("name = ?", "Empty").With("Members").Get(context.Background())
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(teams))
	}
	if teams[0].Members == nil {
		t.Fatal("Members is nil after no-related-ids early return, want empty non-nil slice")
	}
	assertEmptyJSONArray(t, "Members", teams[0].Members)
}

func TestLoadM2M_NoParentIDs_IsEmptySlice(t *testing.T) {
	cleanup := withM2MDB(t)
	defer cleanup()

	// Unsaved parents carry a zero id, so the loader takes its
	// no-parent-ids early return without touching the database.
	models := []Team{{Name: "ghost"}}
	q := newQuery[Team]().With("Members")
	if err := q.loadRelations(context.Background(), &models); err != nil {
		t.Fatalf("loadRelations failed: %v", err)
	}
	if models[0].Members == nil {
		t.Fatal("Members is nil after no-parent-ids early return, want empty non-nil slice")
	}
	assertEmptyJSONArray(t, "Members", models[0].Members)
}
