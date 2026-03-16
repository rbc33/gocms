package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/mailersend/mailersend-go"
	"github.com/rbc33/gocms/common"
	"github.com/rbc33/gocms/database"
	"github.com/rbc33/gocms/views"
	"github.com/rs/zerolog/log"
)

func verifyRecaptchaV3(ctx context.Context, secret, token, expectedAction string) error {
	body := url.Values{"secret": {secret}, "response": {token}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://www.google.com/recaptcha/api/siteverify",
		strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("error building reCAPTCHA request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("error contacting reCAPTCHA API: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Success    bool     `json:"success"`
		Score      float64  `json:"score"`
		Action     string   `json:"action"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("error decoding reCAPTCHA response: %w", err)
	}

	if !result.Success {
		return fmt.Errorf("reCAPTCHA verification failed: %v", result.ErrorCodes)
	}

	if result.Action != expectedAction {
		return fmt.Errorf("unexpected action: got %q, want %q", result.Action, expectedAction)
	}

	if result.Score < 0.5 {
		return fmt.Errorf("low reCAPTCHA score: %.2f", result.Score)
	}

	log.Info().Msgf("reCAPTCHA v3 validation succeeded. Score: %.2f", result.Score)
	return nil
}

func validateEmail(email string) error {
	_, err := mail.ParseAddress(email)
	if err != nil {
		return fmt.Errorf("invalid email format: %s", email)
	}

	return nil
}

// renderErrorPage renderiza la página de error. HTMX reemplazará el contenido del target.
func renderErrorPage(c *gin.Context, email string, originalErr error) {
	log.Error().Err(originalErr).Str("email_attempted", email).Msg("Contact form submission error")
	if renderErr := TemplRender(c, http.StatusOK, views.MakeContactFailure(email, originalErr.Error())); renderErr != nil {
		log.Error().Err(renderErr).Msg("Failed to render contact failure page")
		// Fallback si renderizar la página de error específica falla
		c.String(http.StatusInternalServerError, "An error occurred processing your request, and we could not display the specific error message.")
	}
}

func sendEmail(email, name, subject, message string, c *gin.Context) error {
	mailersendAPIKey := os.Getenv("MAILERSEND_API_KEY")
	recipientEmail := os.Getenv("RECIPIENT_EMAIL")            // Your email address to receive contact messages
	verifiedSenderEmail := os.Getenv("VERIFIED_SENDER_EMAIL") // An email address you've verified in MailerSend

	if mailersendAPIKey == "" || recipientEmail == "" || verifiedSenderEmail == "" {
		error := fmt.Errorf("missing one or more MailerSend environment variables")
		renderErrorPage(c, email, fmt.Errorf("server email configuration is incomplete"))
		return error
	}

	// Create an instance of the mailersend client
	ms := mailersend.NewMailersend(mailersendAPIKey)

	// Create a context with a timeout for the API call
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second) // Adjust timeout as needed
	defer cancel()                                                          // Ensures the context is cancelled when the function exits

	// Define the 'From' sender (must be a verified sender in MailerSend)
	from := mailersend.From{
		Name:  name,                // The user's name from the form
		Email: verifiedSenderEmail, // Your verified sender email in MailerSend
	}

	// Define the recipient (your email address)
	recipients := []mailersend.Recipient{
		{
			Email: recipientEmail,
			// Name: "Your Name for Contact Form", // Optional: If you want a specific name for yourself
		},
	}

	// Create the email message
	msg := ms.Email.NewMessage()

	msg.SetFrom(from)
	msg.SetRecipients(recipients)
	msg.SetSubject("GoCMS Contact: " + subject)
	msg.SetReplyTo(mailersend.Recipient{Email: email})

	htmlBody := fmt.Sprintf(`
            <p><strong>Name:</strong> %s</p>
            <p><strong>Email (from contact form):</strong> %s</p>
            <p><strong>Subject:</strong> %s</p>
            <hr>
            <p><strong>Message:</strong></p>
            <p style="white-space: pre-wrap;">%s</p>
        `, name, email, subject, message)
	msg.SetHTML(htmlBody)

	textBody := fmt.Sprintf("Name: %s\nEmail (from contact form): %s\nSubject: %s\n\nMessage:\n%s",
		name, email, subject, message)
	msg.SetText(textBody)

	res, err := ms.Email.Send(ctx, msg)
	if err != nil {
		bodyBytes, readErr := io.ReadAll(res.Body)
		if readErr != nil {
			log.Error().Err(readErr).Msg("Failed to read MailerSend API response body")
		} else {
			log.Error().
				Int("status_code", res.StatusCode).
				Bytes("body", bodyBytes).
				Msg("MailerSend API response error details")
		}
		res.Body.Close()

	}
	return err
}

func makeContactFormHandler() func(*gin.Context) {
	err := godotenv.Load()
	if err != nil {
		log.Error().Msgf("Error loading .env file")
	}
	return func(c *gin.Context) {
		if err := c.Request.ParseForm(); err != nil {
			log.Error().Msgf("could not parse form %v", err)
			renderErrorPage(c, "unknown", fmt.Errorf("could not parse form: %w", err))
			return
		}

		email := c.Request.FormValue("email")
		name := c.Request.FormValue("name")
		subject := c.Request.FormValue("subject")
		message := c.Request.FormValue("message")
		recaptcha_response := c.Request.FormValue("g-recaptcha-response")

		// Make the request to Google's API only if user
		// configured recatpcha settings
		if (len(common.Settings.RecaptchaSecret) > 0) && (len(common.Settings.RecaptchaSiteKey) > 0) {
			ctx := c.Request.Context()
			err := verifyRecaptchaV3(ctx, common.Settings.RecaptchaSecret, recaptcha_response, "contact_submit")
			if err != nil {
				// El error ya se loguea dentro de verifyRecaptcha si es necesario o aquí en renderErrorPage
				renderErrorPage(c, email, err)
				return
			}
		}

		err := validateEmail(email)
		if err != nil {
			renderErrorPage(c, email, err)
			return
		}

		// Make sure name and message is reasonable
		if len(name) > 200 {
			renderErrorPage(c, email, fmt.Errorf("name too long (200 chars max)"))
			return
		}

		if len(message) > 10000 {
			// El mensaje de error original decía 1000, pero el código verifica 10000.
			renderErrorPage(c, email, fmt.Errorf("message too long (10000 chars max)"))
			return
		}
		err = sendEmail(email, name, subject, message, c)
		if err != nil {
			renderErrorPage(c, email, err)
			return
		}

		if renderErr := TemplRender(c, http.StatusOK, views.MakeContactSuccess(email, name)); renderErr != nil {
			log.Error().Err(renderErr).Msg("Failed to render contact success page")
			c.String(http.StatusInternalServerError, "An error occurred while processing your request.")
		}
	}
}

// TODO : This is a duplicate of the index handler... abstract
func contactHandler(c *gin.Context, db database.Database) ([]byte, error) {
	return renderHtml(c, views.MakeContactPage(common.Settings.AppNavbar.Links, common.Settings.RecaptchaSiteKey, common.Settings.AppNavbar.Dropdowns))
}
