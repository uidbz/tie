package webservice

const (
	IdCreateAccount = "CreateAccount"
)

type CreateAccountRequest struct {
	Request
	Email          string
	Password       string
	ValidationCode string
	validate       func(user, code string) bool
	addUser        func(username, password string)
}

type CreateAccountReply struct {
	ReplyStatus
}

func (request *CreateAccountRequest) Reply(env *Environment) (Reply, error) {
	reply := CreateAccountReply{}

	if request.validate(request.Email, request.ValidationCode) {
		request.addUser(request.Email, request.Password)
		reply.Success = true
	} else {
		reply.Success = false
		reply.Message = "Wrong validation code provided"
	}

	return Reply{request.Id, reply}, nil
}

func (reply *Reply) DataCreateAccount() *CreateAccountReply {
	return reply.ReplyStructPtr.(*CreateAccountReply)
}

func NewCreateAccountRequest() *CreateAccountRequest {
	request := &CreateAccountRequest{}
	request.Id = IdCreateAccount
	request.ReplyStructPtr = &CreateAccountReply{}

	return request
}
