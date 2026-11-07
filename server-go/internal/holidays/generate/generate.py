"""Generates holidays.json.gz, the public holiday calendars bundled into kuma-analytics.

The data comes from the python-holidays library (https://github.com/vacanza/holidays), which
computes each country's public holidays, including lunar ones and observed or substituted days
off where the country announces them. Run it from server-go/ with Docker, which pins the
library's version:

    docker run --rm -v "$PWD/internal/holidays:/out" python:3.13-slim sh -c \
        'pip install -q holidays==0.108 && python /out/generate/generate.py /out/holidays.json.gz'

Then commit the new file. Admins can add or remove dates per country in the web UI, for days
the library doesn't know yet, such as China's adjusted holidays announced each winter.
"""

import datetime
import gzip
import json
import re
import sys

import holidays
from holidays.registry import COUNTRIES

FIRST_YEAR = 2024
# The library computes most countries only up to here.
LAST_YEAR = 2035

# The library's default category is "public". Where that isn't the list that offices and banks
# close for, the right one is named here, with the official list it matches.
CATEGORIES = {
    # The government's "general holidays" (1823.gov.hk), not the shorter statutory holidays.
    "HK": ("optional",),
}


def country_names() -> dict:
    # The registry maps modules to (ClassName, alpha-2, alpha-3, ...), such as HongKong.
    names = {}
    for class_name, code, *_ in COUNTRIES.values():
        names[code] = re.sub(r"(?<=[a-z])(?=[A-Z])", " ", class_name)
    return names


def main(path: str) -> None:
    countries = {}
    names = country_names()
    years = range(FIRST_YEAR, LAST_YEAR + 1)
    for code in sorted(holidays.list_supported_countries(include_aliases=False)):
        options = {"years": years}
        if code in CATEGORIES:
            options["categories"] = CATEGORIES[code]
        try:
            entity = holidays.country_holidays(code, language="en_US", **options)
        except Exception:
            entity = holidays.country_holidays(code, **options)
        dates = {day.isoformat(): name for day, name in sorted(entity.items())}
        if dates:
            countries[code] = {"name": names.get(code, code), "holidays": dates}

    data = {
        "source": f"python-holidays {holidays.__version__}",
        "generated": datetime.date.today().isoformat(),
        "firstYear": FIRST_YEAR,
        "lastYear": LAST_YEAR,
        "countries": countries,
    }
    with gzip.open(path, "wt", encoding="utf-8", compresslevel=9) as f:
        json.dump(data, f, ensure_ascii=False, separators=(",", ":"), sort_keys=True)
    total = sum(len(c["holidays"]) for c in countries.values())
    print(f"{len(countries)} countries, {total} holidays, {FIRST_YEAR}-{LAST_YEAR}, {data['source']}")


if __name__ == "__main__":
    main(sys.argv[1])
