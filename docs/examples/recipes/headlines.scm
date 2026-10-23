;; Reads the headlines of a page: its second-level headings, in order.

;; The text between the first pair of quotes on a snapshot line.
(define (quoted line)
  (let* ((start (+ 1 (string-index line "\"")))
         (rest (substring line start)))
    (substring rest 0 (string-index rest "\""))))

(define (headlines snapshot)
  (let loop ((lines (string-split snapshot "\n")) (found '()))
    (if (null? lines)
        (reverse found)
        (let ((line (car lines)))
          (loop (cdr lines)
                (if (and (string-contains? line "- heading ")
                         (string-contains? line "[level=2]"))
                    (cons (quoted line) found)
                    found))))))

(define-recipe "headlines"
  "Read the headlines of a page: its second-level headings, one per line."
  '(("url" "string" "The page to read")
    ("limit" "number" "How many headlines at most" 10))
  (lambda (url limit)
    (navigate url)
    (let ((all (headlines (snapshot))))
      (string-join (take all (min limit (length all))) "\n"))))
