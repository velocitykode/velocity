// Package inflect holds the English inflection and delimiter-case helpers
// shared by str, orm and console: pluralisation, singularisation and
// snake_case / kebab-case conversion.
//
// It lives below str so the ORM's table-name derivation and the console
// generators reach these helpers without importing str, which links the
// Markdown engine and its dependencies into every binary that imports it.
// The str package re-exports every function here under its public names;
// application code uses str.
package inflect
