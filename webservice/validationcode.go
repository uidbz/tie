package webservice

import (
	"errors"
	"net/mail"
	"net/smtp"
	"strings"

	"git.sr.ht/~uid/tie/tiedb"
)

const (
	IdValidationCode = "ValidationCode"
)

type ValidationCodeRequest struct {
	Request
	code              string
	Email             string
	getValidationCode func(email string) string
}

type ValidationCodeReply struct {
	ReplyStatus
}

type loginAuth struct {
	username, password string
}

func LoginAuth(username, password string) smtp.Auth {
	return &loginAuth{username, password}
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", []byte{}, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		switch string(fromServer) {
		case "Username:":
			return []byte(a.username), nil
		case "Password:":
			return []byte(a.password), nil
		default:
			return nil, errors.New("Unkown fromServer")
		}
	}
	return nil, nil
}

func ReadMailSettings(result *tiedb.StringSliceSet, code string) MailSettings {
	mail := MailSettings{}
	for i, x := range result.Value1 {
		switch x {
		case SERVER:
			mail.Server = result.Value2[i]
		case FROM:
			mail.From = result.Value2[i]
		case SUBJECT:
			mail.Subject = result.Value2[i]
		case USERNAME:
			mail.Username = result.Value2[i]
		case PASSWORD:
			mail.Password = result.Value2[i]
		case MESSAGE:
			mail.Message = result.Value2[i]
		}
	}

	mail.Subject = strings.ReplaceAll(mail.Subject, "[CODE]", code)
	mail.Message = strings.ReplaceAll(mail.Message, "[CODE]", code)

	return mail
}

func (request *ValidationCodeRequest) Reply(username string, account func(string) tiedb.Collection) (Reply, error) {
	reply := ValidationCodeReply{}

	email, errEmail := mail.ParseAddress(request.Email)

	if errEmail != nil {
		reply.Success = false
		reply.Message = "Bad e-mail address provided."
	} else {
		found, result := account(MAILSETTINGSDB).Get(MAIL, "")
		if found {
			code := request.getValidationCode(email.Address)
			mail := ReadMailSettings(result, code)
			auth := LoginAuth(mail.Username, mail.Password)
			to := []string{email.Address}
			msg := []byte(
				"To: " + email.Address + "\r\n" +
					"From: " + mail.From + "\r\n" +
					"Subject: " + mail.Subject + "\r\n" +
					"\r\n" + mail.Message)

			err := smtp.SendMail(mail.Server, auth, mail.From, to, msg)
			if err != nil {
				reply.Success = false
				reply.Message = "Something went wrong, please try again later"
			} else {
				reply.Success = true
			}
		} else {
			reply.Success = false
			reply.Message = "Mail settings not set"
		}
	}

	return Reply{request.Id, reply}, nil
}

func (reply *Reply) DataValidationCode() *ValidationCodeReply {
	return reply.ReplyStructPtr.(*ValidationCodeReply)
}

func NewValidationCodeRequest() *ValidationCodeRequest {
	request := &ValidationCodeRequest{}
	request.Id = IdValidationCode
	request.ReplyStructPtr = &ValidationCodeReply{}

	return request
}
