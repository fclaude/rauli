;; Fills in a sign-up form and submits it. The form has a Name field, an Email
;; field and a button called Sign up.

(define-recipe "signup"
  "Fill in the sign-up form at a page and submit it. Returns what the next page says."
  '(("url" "string" "The address of the form")
    ("name" "string" "The name to enter")
    ("email" "string" "The email address to enter"))
  (lambda (url name email)
    (navigate url)
    (let* ((page (snapshot))
           (name-ref (find-ref page "textbox \"Name\""))
           (email-ref (find-ref page "textbox \"Email\""))
           (button (find-ref page "button \"Sign up\"")))
      (if (not (and name-ref email-ref button))
          (error "the page does not have a Name field, an Email field and a Sign up button"))
      (type-text name-ref name)
      (type-text email-ref email)
      (click button)
      (text))))
