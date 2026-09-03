package node

// A throwaway host key for the test server next door. It is a test fixture and
// guards nothing: it exists so the client has a real key to check.
var testHostKey = []byte(`-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACBp+cSZe6nz/7A5SaUeql0UghVMqUWZ9CWL6UstGrEesAAAALAUSpKwFEqS
sAAAAAtzc2gtZWQyNTUxOQAAACBp+cSZe6nz/7A5SaUeql0UghVMqUWZ9CWL6UstGrEesA
AAAEA6I85NPS0l+pMnwUdEXHHQPijDz5J083USQvKk8uf9DWn5xJl7qfP/sDlJpR6qXRSC
FUypRZn0JYvpSy0asR6wAAAALWFtaW5AYW1pbi1BU1VTLVRVRi1HYW1pbmctQTE3LUZBNz
A2SVUtRlg3MDZJVQ==
-----END OPENSSH PRIVATE KEY-----
`)
