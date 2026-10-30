"""Read-only print metadata report. Does not read documents, OTPs or credentials."""
import json
import sqlite3

with sqlite3.connect("file:/var/lib/printcatalyst-kiosk/print-catalyst.db?mode=ro", uri=True) as db:
    db.row_factory = sqlite3.Row
    orders = db.execute("SELECT id,status,payment_method FROM orders WHERE submitted_at>0 ORDER BY submitted_at DESC LIMIT 5").fetchall()
    for order in orders:
        print("\nORDER", order["id"][:8], order["status"], order["payment_method"])
        queries = {
            "documents": "SELECT substr(d.id,1,8) AS document,d.mime_type,d.page_count,CASE WHEN d.original_filename LIKE 'Photo collage-%' OR d.original_filename LIKE 'Merged images-%' THEN 1 ELSE 0 END AS composed,COUNT(l.id) AS print_lines FROM documents d LEFT JOIN order_lines l ON l.document_id=d.id AND l.order_id=d.order_id WHERE d.order_id=? GROUP BY d.id",
            "submissions": "SELECT substr(l.document_id,1,8) AS document,l.copies,l.page_range_start,l.page_range_end,j.state,j.progress,j.progress_detail,j.queue_name,j.job_id,j.error,j.updated_at AS submission_updated_at FROM order_lines l LEFT JOIN print_submissions j ON j.line_id=l.id WHERE l.order_id=? ORDER BY l.id",
            "preparation": "SELECT state,error FROM kiosk_preparations WHERE order_id=?",
            "release": "SELECT release_mode,claimed_at FROM kiosk_releases WHERE order_id=?",
        }
        for label, query in queries.items():
            print(label, json.dumps([dict(row) for row in db.execute(query, (order["id"],))]))
