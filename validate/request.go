package validate

// FormRequest defines validation rules for a request. Implement this
// interface on a struct to create reusable, self-validating request types —
// the request struct itself owns its rules.
//
// Deprecated: the shape is unchanged, but new code should import
// github.com/velocitykode/velocity/validation for rule types. This shim
// exists because Form[T] needs a *router.Context and therefore cannot live
// in the leaf validation package.
//
//	type CreatePostRequest struct {
//	    Title string `json:"title"`
//	    Body  string `json:"body"`
//	}
//
//	func (r *CreatePostRequest) Rules() Rules {
//	    return Rules{
//	        "title": {"required", "min:3"},
//	        "body":  {"required", "min:10"},
//	    }
//	}
type FormRequest interface {
	Rules() Rules
}

// WithMessages can be implemented alongside FormRequest to provide custom
// error messages per field and rule.
//
// Deprecated: use validation.Messages directly. WithMessages is preserved
// only so existing Form[T] callers keep compiling.
//
//	func (r *CreatePostRequest) ValidationMessages() Messages {
//	    return Messages{
//	        "title.required": "Please provide a title",
//	    }
//	}
type WithMessages interface {
	ValidationMessages() Messages
}

// WithAuthorization can be implemented alongside FormRequest to authorize
// the request before validation runs.
//
// Deprecated: gate authorization in middleware or use ctx.Authorize / ctx.Can
// in the handler. The hook is kept wired only for backwards compatibility.
//
//	func (r *CreatePostRequest) Authorize() bool {
//	    return true // or check permissions
//	}
type WithAuthorization interface {
	Authorize() bool
}
