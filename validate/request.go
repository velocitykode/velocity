package validate

// FormRequest defines validation rules for a request.
// Implement this interface on a struct to create reusable, self-validating
// request types — the request struct itself owns its rules.
//
// Future usage:
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
//
//	// In handler:
//	var req CreatePostRequest
//	if errors := validate.Form(ctx, &req); errors.HasErrors() { ... }
type FormRequest interface {
	Rules() Rules
}

// WithMessages can be implemented alongside FormRequest to provide
// custom error messages per field and rule.
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
//	func (r *CreatePostRequest) Authorize() bool {
//	    return true // or check permissions
//	}
type WithAuthorization interface {
	Authorize() bool
}
