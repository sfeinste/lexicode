// Package ports declares the frozen port interfaces and their domain types (contracts §2).
//
// A port is the only way the kernel and the services above it are allowed to reach an adapter.
// Two rules keep them honest (architecture §4):
//
//   - A port's method set is the whole contract. No type assertions to concrete module types
//     outside cmd/lexicode.
//   - Ports return domain types, never adapter types.
//
// All eight interfaces are declared here from story S02 so that the Kernel's registration API is
// frozen from day one, but each currently declares only ID(). The story that fills in a port's
// real method set — transcribed verbatim from contracts §2, which is the authority — is named in
// that port's doc comment. Do not invent methods ahead of that story.
package ports
