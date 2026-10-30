import contextlib
import importlib.util
import io
from pathlib import Path
import subprocess
import unittest
from urllib.parse import parse_qs, urlsplit

spec = importlib.util.spec_from_file_location("epson", Path(__file__).with_name("configure-epson.py"))
epson = importlib.util.module_from_spec(spec)
spec.loader.exec_module(epson)
URI = "ipp://192.168.1.50/ipp/print"
QUEUE = "Epson_EM_C800"


class FakeCUPS:
    def __init__(self, existing=None, jobs="", active="inactive", persist=True):
        self.queues = existing or {}
        self.jobs, self.active, self.persist = jobs, active, persist
        self.calls = []

    def __call__(self, args, check=True):
        self.calls.append(args)
        output = ""
        if args[0].endswith("lpstat"):
            output = "\n".join(f"device for {name}: {value}" for name, value in self.queues.items()) if "-v" in args else self.jobs
        elif args[0].endswith("systemctl"):
            output = self.active
        elif args[0].endswith("lpadmin"):
            if self.persist:
                self.queues[args[args.index("-p")+1]] = args[args.index("-v")+1]
        else:
            raise AssertionError(args)
        return subprocess.CompletedProcess(args, 0, output, "")


def probe_ok(uri):
    return "EPSON EM-C800 Series", ["application/pdf", "image/pwg-raster"]


class EpsonTests(unittest.TestCase):
    def configure(self, runner, **kwargs):
        with contextlib.redirect_stdout(io.StringIO()):
            epson.configure(URI, QUEUE, runner=runner, prober=probe_ok, uid=0, **kwargs)

    def test_no_mutation_in_check_mode(self):
        runner = FakeCUPS()
        self.configure(runner)
        self.assertFalse(any(c[0].endswith("lpadmin") for c in runner.calls))

    def test_new_queue_driverless_separate_nonwaiting_jobs_and_stop_on_error(self):
        runner = FakeCUPS()
        self.configure(runner, apply=True)
        command = next(c for c in runner.calls if c[0].endswith("lpadmin"))
        self.assertIn("everywhere", command)
        self.assertIn("printer-error-policy=stop-printer", command)
        self.assertIn("printer-is-shared=false", command)
        self.assertIn("localhost:631", command)
        self.assertEqual(parse_qs(urlsplit(runner.queues[QUEUE]).query), {"waitjob": ["false"], "waitprinter": ["false"]})
        self.assertFalse(any(c[0].endswith(("/lp", "/cancel")) for c in runner.calls))

    def test_existing_keeps_driver_tls_parameters_and_does_not_resume(self):
        runner = FakeCUPS({QUEUE: URI + "?version=2.0&encryption=required&waitjob=true"})
        self.configure(runner, apply=True)
        command = next(c for c in runner.calls if c[0].endswith("lpadmin"))
        self.assertNotIn("-m", command)
        self.assertNotIn("-E", command)
        self.assertIn("encryption=required", runner.queues[QUEUE])
        self.assertIn("version=2.0", runner.queues[QUEUE])

    def test_busy_active_and_foreign_queue_do_not_change_configuration(self):
        for runner in [FakeCUPS({QUEUE: URI}, jobs="Epson_EM_C800-42 pending"),
                       FakeCUPS(active="active"), FakeCUPS(active="activating"),
                       FakeCUPS({QUEUE.lower(): "usb://Canon/LBP2900"}),
                       FakeCUPS({QUEUE: "ipp://192.168.1.51/ipp/print"})]:
            with self.subTest(runner=runner), self.assertRaises(epson.SetupError):
                self.configure(runner, apply=True)
            self.assertFalse(any(c[0].endswith("lpadmin") for c in runner.calls))

    def test_post_apply_configuration_is_verified(self):
        with self.assertRaises(epson.SetupError):
            self.configure(FakeCUPS(persist=False), apply=True)

    def test_invalid_uris_and_queue_names(self):
        for uri in ["usb://Epson/printer", "socket://192.168.1.50", "ipp://127.0.0.1/ipp/print",
                    "ipp://8.8.8.8/ipp/print", "ipp://user:secret@192.168.1.50/ipp/print",
                    URI+"?waitjob=true", URI+"#fragment", URI+"\n", URI+";command",
                    "ipp://192.168.1.50:0/ipp/print"]:
            with self.subTest(uri=uri), self.assertRaises(epson.SetupError):
                epson.endpoint(uri)
        with self.assertRaises(epson.SetupError):
            epson.configure(URI, "bad;queue", runner=FakeCUPS(), prober=probe_ok)

    def test_probe_does_not_print_and_checks_actual_model_and_formats(self):
        sample = "printer-make-and-model (textWithoutLanguage) = EPSON EM-C800 Series\ndocument-format-supported (1setOf mimeMediaType) = application/pdf,image/pwg-raster\n"
        def runner(args):
            request = Path(args[-1]).read_text()
            self.assertIn("OPERATION Get-Printer-Attributes", request)
            self.assertNotIn("OPERATION Print-Job", request)
            return subprocess.CompletedProcess(args, 0, sample, "")
        model, formats = epson.probe(URI, runner)
        self.assertIn("EM-C800", model)
        self.assertIn("application/pdf", formats)
        for bad in [sample.replace("EM-C800", "EM-C8000"), sample.replace("EPSON", "Other"),
                    sample.replace("application/pdf,image/pwg-raster", "application/octet-stream"),
                    sample+sample]:
            with self.subTest(bad=bad), self.assertRaises(epson.SetupError):
                epson.probe(URI, lambda args: subprocess.CompletedProcess(args, 0, bad, ""))

    def test_empty_cups_is_allowed_but_connection_failure_is_not(self):
        self.assertEqual(epson.queue_uris(lambda args, **kw: subprocess.CompletedProcess(args, 1, "", "lpstat: No destinations added.\n")), {})
        with self.assertRaises(epson.SetupError):
            epson.queue_uris(lambda args, **kw: subprocess.CompletedProcess(args, 1, "", "Unable to connect"))

    def test_profile_is_idempotent(self):
        uri = URI + "?waitjob=yes&waitprinter=true&waitjob=true&version=2.0"
        optimized = epson.fast_uri(uri)
        self.assertEqual(optimized, epson.fast_uri(optimized))
        self.assertEqual(parse_qs(urlsplit(optimized).query)["waitjob"], ["false"])


if __name__ == "__main__":
    unittest.main()
