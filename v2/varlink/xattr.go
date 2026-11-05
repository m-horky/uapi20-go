package varlink

// XattrUserVarlink is the name of the extended attribute that tags AF_UNIX
// socket inodes as participating in Varlink communication.
const XattrUserVarlink = "user.varlink"

// Roles recorded in the user.varlink extended attribute of Varlink sockets.
const (
	// RoleClient tags the connecting end of a connection.
	RoleClient = "client"
	// RoleServer tags the serving end of a connection, i.e. a socket
	// obtained via accept().
	RoleServer = "server"
	// RoleListen tags a listening socket.
	RoleListen = "listen"
	// RoleEntrypoint tags the entrypoint socket inode bound into the file
	// system that clients connect to.
	RoleEntrypoint = "entrypoint"
)

// RegistryDirectory is the well-known directory in which public Varlink
// services register their entrypoint sockets.
const RegistryDirectory = "/run/varlink/registry"
