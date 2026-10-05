"""Private renderer protocol: JSON header + newline + raw source on stdin.
Produces JSON metadata + newline + a complete PDF, or fails without PDF output.
Never accepts filenames/commands from documents. Executed by the Go service.
"""
import base64
import io
import json
import math
import os
import sys
import unicodedata
import warnings
from xml.sax.saxutils import escape

import pypdfium2 as pdfium
from PIL import Image, ImageOps
from reportlab.pdfgen import canvas
from reportlab.lib.utils import ImageReader
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.platypus import Paragraph
from reportlab.lib.styles import ParagraphStyle

MAX_BYTES = 50 * 1024 * 1024
MAX_PIXELS = 25_000_000
DPI = 300
PAPERS = {"A3": (841.89,1190.55), "A4": (595.28,841.89), "A5": (419.53,595.28),
          "A6": (297.64,419.53), "LETTER": (612,792), "LEGAL": (612,1008)}
Image.MAX_IMAGE_PIXELS = MAX_PIXELS
warnings.simplefilter("error", Image.DecompressionBombWarning)


def sheet_size(paper):
    key = paper.upper()
    aliases = {"ISO_A3_297X420MM":"A3", "ISO_A4_210X297MM":"A4", "ISO_A5_148X210MM":"A5",
               "ISO_A6_105X148MM":"A6", "NA_LETTER_8.5X11IN":"LETTER", "NA_LEGAL_8.5X14IN":"LEGAL"}
    if key not in PAPERS:
        key = aliases.get(key, "")
    if key not in PAPERS:
        raise ValueError("unsupported paper")
    return PAPERS[key]


def safe_image(body):
    image = Image.open(io.BytesIO(body))
    if image.width * image.height > MAX_PIXELS or image.n_frames != 1:
        raise ValueError("image limit")
    image.load()
    image = ImageOps.exif_transpose(image).convert("RGBA")
    white = Image.new("RGBA", image.size, "white")
    white.alpha_composite(image)
    return white.convert("RGB")


def draw_fit(out, image, rect, monochrome):
    x,y,width,height = rect
    if monochrome:
        image = image.convert("L")
    scale = min(width/image.width, height/image.height)
    w,h = image.width*scale, image.height*scale
    out.drawImage(ImageReader(image), x+(width-w)/2, y+(height-h)/2, w,h, mask=None)


def invoice_fonts(paths):
    result = []
    for index,path in enumerate(paths):
        if not os.path.isabs(path) or not os.path.isfile(path):
            continue
        name = "InvoiceFont" + str(index)
        font = TTFont(name, path, shapable=True)
        pdfmetrics.registerFont(font)
        result.append((name,font.face.charToGlyph))
    if not result:
        raise ValueError("invoice fonts unavailable")
    return result


def markup(text, fonts):
    # Select a font for complete space-separated words, keeping combining
    # marks and shaping sequences together. Missing glyphs fail visibly.
    words = text.replace("\t", "    ").split(" ")
    runs = []
    for word in words:
        chars = [ord(c) for c in word if c not in ("\u200c", "\u200d")]
        chosen = next((name for name,cmap in fonts if all(c in cmap for c in chars)), None)
        if chosen is None:
            raise ValueError("invoice glyph unavailable")
        runs.append('<font name="%s">%s</font>' % (chosen, escape(word)))
    return " ".join(runs) or "&#160;"


def render_invoice(out, header, body, width, height):
    text = body.decode("utf-8", errors="strict")
    if len(text) > 100_000:
        raise ValueError("invoice too long")
    fonts = invoice_fonts(header["fonts"])
    style = ParagraphStyle("invoice",fontName=fonts[0][0],fontSize=9,leading=12,shaping=True,splitLongWords=True)
    margin = 18
    y = height-margin
    pages = 1
    if header.get("logo"):
        logo = safe_image(base64.b64decode(header["logo"],validate=True))
        draw_fit(out,logo,(margin,y-48,width-2*margin,48),True)
        y -= 56
    for line in text.splitlines():
        blocks = [Paragraph(markup(line,fonts),style)]
        while blocks:
            paragraph = blocks.pop(0)
            _,h = paragraph.wrap(width-2*margin,y-margin)
            if h <= y-margin:
                paragraph.drawOn(out,margin,y-h); y -= h+3
                continue
            parts = paragraph.split(width-2*margin,y-margin)
            if parts:
                head = parts.pop(0)
                _,h = head.wrap(width-2*margin,y-margin)
                head.drawOn(out,margin,y-h)
                blocks = parts+blocks
            else:
                if y == height-margin:
                    raise ValueError("invoice text does not fit")
                blocks.insert(0,paragraph)
            out.showPage(); pages += 1; y=height-margin
            if pages>100:
                raise ValueError("invoice page limit")
    out.showPage()
    return pages, False


def render(header, body):
    width,height = sheet_size(header["paper"])
    output = io.BytesIO()
    out = canvas.Canvas(output,pagesize=(width,height),pageCompression=1,invariant=1)
    out.setTitle("Print Catalyst prepared output")
    out.setAuthor("")
    document = None
    if header.get("invoice"):
        count,landscape = render_invoice(out,header,body,width,height)
    else:
        image = None
        if header["mime"] == "application/pdf":
            document = pdfium.PdfDocument(body)
            document.init_forms()
            total = len(document)
        elif header["mime"] in ("image/png","image/jpeg"):
            image = safe_image(body); total=1
        else:
            raise ValueError("unsupported source")
        pages = header["pages"]
        if total != header["source_pages"] or not pages or len(pages)>1000 or pages!=sorted(set(pages)) or any(type(p)!=int or p<1 or p>total for p in pages):
            raise ValueError("invalid source pages")
        if image is not None:
            first_width,first_height = image.size
        else:
            page=document[pages[0]-1]
            first_width,first_height=page.get_size();page.close()
        orientation=header["orientation"]
        if orientation not in ("auto","portrait","landscape"):
            raise ValueError("invalid orientation")
        landscape=orientation=="landscape" or (orientation=="auto" and first_width>first_height)
        if landscape:
            width,height=height,width
        nup=header["nup"]
        if nup not in (1,2,4):
            raise ValueError("invalid layout")
        cols,rows={1:(1,1),2:(1,2),4:(2,2)}[nup]
        out.setPageSize((width,height))
        count=0
        # A small safe content margin avoids losing edge content on typical
        # office printers. Real printer imageable areas still need qualification.
        margin=12
        cell_w,cell_h=(width-2*margin)/cols,(height-2*margin)/rows
        for index,number in enumerate(pages):
            page=bitmap=None
            try:
                current=image
                if document is not None:
                    page=document[number-1]
                    pw,ph=page.get_size()
                    if not all(math.isfinite(v) and v>0 for v in (pw,ph)):
                        raise ValueError("invalid page dimensions")
                    scale=min(cell_w/pw,cell_h/ph)*DPI/72
                    if math.ceil(pw*scale)*math.ceil(ph*scale)>MAX_PIXELS:
                        raise ValueError("page raster limit")
                    bitmap=page.render(scale=scale,draw_annots=True)
                    current=bitmap.to_pil().convert("RGB")
                slot=index%nup
                rect=(margin+(slot%cols)*cell_w, height-margin-(slot//cols+1)*cell_h,cell_w,cell_h)
                draw_fit(out,current,rect,header["colour"]=="monochrome")
                if slot==nup-1 or index==len(pages)-1:
                    out.showPage();count+=1
            finally:
                if bitmap is not None: bitmap.close()
                if page is not None: page.close()
        if document is not None: document.close()
    out.save()
    data=output.getvalue()
    if len(data)>MAX_BYTES:
        raise ValueError("prepared PDF too large")
    # Reopen to verify actual page count and geometry, including invoice pagination.
    check=pdfium.PdfDocument(data)
    if len(check)!=count or count<1:
        raise ValueError("invalid rendered output")
    for i in range(count):
        p=check[i];size=p.get_size();p.close()
        if abs(size[0]-width)>1 or abs(size[1]-height)>1:
            raise ValueError("output paper mismatch")
    check.close()
    return {"pages":count,"landscape":landscape,"dpi":DPI},data


def render_preview(header, body):
    doc=pdfium.PdfDocument(body);doc.init_forms()
    number=header["page"]
    if type(number)!=int or number<1 or number>len(doc):
        raise ValueError("invalid preview page")
    page=doc[number-1];width,height=page.get_size()
    if not all(math.isfinite(v) and v>0 for v in (width,height)):
        raise ValueError("invalid page size")
    bitmap=page.render(scale=900/max(width,height),draw_annots=True)
    image=bitmap.to_pil().convert("RGB");out=io.BytesIO();image.save(out,format="PNG")
    bitmap.close();page.close();doc.close()
    return {"width":image.width,"height":image.height},out.getvalue()

def main():
    if sys.platform=="linux":
        import resource
        import os
        memory = min(1536<<20, max(256<<20, os.sysconf("SC_PHYS_PAGES") * os.sysconf("SC_PAGE_SIZE") // 2))
        resource.setrlimit(resource.RLIMIT_AS,(memory,memory))
        resource.setrlimit(resource.RLIMIT_CPU,(90,90))
    raw=sys.stdin.buffer.readline((1<<20)+1)
    if len(raw)>1<<20 or not raw.endswith(b"\n"):
        raise ValueError("invalid header")
    header=json.loads(raw)
    body=sys.stdin.buffer.read(MAX_BYTES+1)
    if len(body)>MAX_BYTES or not body:
        raise ValueError("invalid source size")
    metadata,pdf=render_preview(header,body) if header.get("preview") else render(header,body)
    sys.stdout.buffer.write(json.dumps(metadata,separators=(",",":")).encode()+b"\n"+pdf)

if __name__=="__main__":
    try:
        main()
    except Exception:
        # Do not copy document content, customer names or filesystem paths to logs.
        sys.stderr.write("Document preparation failed.\n")
        sys.exit(1)
