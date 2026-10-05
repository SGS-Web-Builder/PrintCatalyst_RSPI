import io
import os
import pathlib
import unittest

import pypdfium2 as pdfium
from PIL import Image
from reportlab.pdfgen import canvas
from render import render, render_preview


def source_pdf():
    out=io.BytesIO();pdf=canvas.Canvas(out,pagesize=(200,300),invariant=1)
    for colour in [(1,0,0),(0,1,0),(0,0,1)]:
        pdf.setFillColorRGB(*colour);pdf.rect(0,0,200,300,fill=1,stroke=0);pdf.showPage()
    pdf.save();return out.getvalue()


def header(**overrides):
    value=dict(paper="A4",colour="colour",orientation="portrait",mime="application/pdf",pages=[1,3],source_pages=3,nup=2,invoice=False,fonts=[])
    value.update(overrides);return value


def pixels(data):
    doc=pdfium.PdfDocument(data);page=doc[0];bitmap=page.render(scale=1)
    image=bitmap.to_pil().convert("RGB");bitmap.close();page.close();doc.close();return image


class RendererTests(unittest.TestCase):
    def test_preview_selects_page_and_bounds_dimensions(self):
        metadata, data = render_preview({"page": 2}, source_pdf())
        image = Image.open(io.BytesIO(data))
        self.assertEqual(image.format, "PNG")
        self.assertEqual(image.size, (600, 900))
        self.assertEqual(metadata, {"width": 600, "height": 900})
        self.assertEqual(image.getpixel((300, 450)), (0, 255, 0))

    def test_preview_rejects_invalid_page(self):
        for page in (0, 4, -1, True, "1"):
            with self.subTest(page=page), self.assertRaises(ValueError):
                render_preview({"page": page}, source_pdf())

    def test_selected_pages_and_nup(self):
        metadata,data=render(header(),source_pdf())
        self.assertEqual(metadata["pages"],1)
        image=pixels(data);x=image.width//2
        self.assertGreater(image.getpixel((x,image.height//4))[0],240)
        self.assertLess(image.getpixel((x,image.height//4))[2],15)
        self.assertGreater(image.getpixel((x,3*image.height//4))[2],240)
        self.assertLess(image.getpixel((x,3*image.height//4))[1],15)

    def test_landscape_grayscale_and_four_up(self):
        metadata,data=render(header(orientation="landscape",colour="monochrome",pages=[1,2,3],nup=4),source_pdf())
        self.assertTrue(metadata["landscape"])
        image=pixels(data);self.assertGreater(image.width,image.height)
        rgb=image.getpixel((image.width//4,image.height//4));self.assertLess(max(rgb)-min(rgb),2)
        self.assertEqual(image.getpixel((3*image.width//4,3*image.height//4)),(255,255,255))

    def test_transparent_image_is_white_and_auto_orientation(self):
        image=Image.new("RGBA",(200,100),(255,0,0,0));body=io.BytesIO();image.save(body,format="PNG")
        metadata,data=render(header(mime="image/png",pages=[1],source_pages=1,nup=1,orientation="auto"),body.getvalue())
        self.assertTrue(metadata["landscape"])
        image=pixels(data);self.assertEqual(image.getpixel((image.width//2,image.height//2)),(255,255,255))

    def test_invalid_selection_and_metadata_fail(self):
        for fields in [dict(pages=[3,1]),dict(pages=[0]),dict(pages=[4]),dict(pages=[1,1]),dict(nup=3),dict(source_pages=2),dict(paper="bad")]:
            with self.subTest(fields=fields),self.assertRaises(Exception):render(header(**fields),source_pdf())

    def test_unicode_invoice_pagination_and_literal_markup(self):
        font=os.environ.get("PC_TEST_INVOICE_FONT")
        if not font:
            self.fail("PC_TEST_INVOICE_FONT must identify a font containing Latin and Devanagari glyphs")
        text="श्री Print Catalyst\nOrder <script> & settings\n"+"Document A4 · 2 copies · Total INR 10.00\n"*70
        metadata,data=render(header(paper="A6",invoice=True,mime="text/plain",pages=[1],source_pages=1,nup=1,fonts=[font]),text.encode())
        self.assertGreater(metadata["pages"],1)
        doc=pdfium.PdfDocument(data);self.assertEqual(len(doc),metadata["pages"])
        page=doc[0];textpage=page.get_textpage();extracted=textpage.get_text_range();textpage.close();page.close();doc.close()
        self.assertIn("Print Catalyst",extracted);self.assertIn("<script>",extracted)
        if os.environ.get("PC_RENDER_PREVIEW"):
            path=pathlib.Path(os.environ["PC_RENDER_PREVIEW"]);path.parent.mkdir(parents=True,exist_ok=True)
            pixels(data).save(path)

    def test_missing_glyph_fails_instead_of_printing_boxes(self):
        font=os.environ["PC_TEST_INVOICE_FONT"]
        with self.assertRaises(ValueError):render(header(invoice=True,fonts=[font]),"unsupported \U0010ffff".encode())

if __name__=="__main__":unittest.main()
