/*
 * Ad-hoc C benchmark harness for libucl, matching the operations measured
 * in the Go benchmarks (parse from buffer, emit JSON, emit JSON-compact,
 * emit UCL config).
 *
 * Not derived from upstream test_speed.c (which times a single run of a
 * single large file with clock_gettime and no iteration loop) - this
 * harness loops N times per operation and reports ns/op, to get stable
 * numbers on the small fixture files used elsewhere in this repo's tests.
 *
 * Usage: bench_c <file> <iterations>
 */

#include "ucl.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static double
now_seconds(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (double)ts.tv_sec + ts.tv_nsec / 1e9;
}

static char *
read_file(const char *path, size_t *len_out)
{
	FILE *f = fopen(path, "rb");
	if (!f) {
		perror("fopen");
		exit(1);
	}
	fseek(f, 0, SEEK_END);
	long sz = ftell(f);
	fseek(f, 0, SEEK_SET);
	char *buf = malloc(sz);
	if (fread(buf, 1, sz, f) != (size_t)sz) {
		perror("fread");
		exit(1);
	}
	fclose(f);
	*len_out = (size_t)sz;
	return buf;
}

int
main(int argc, char **argv)
{
	if (argc != 3) {
		fprintf(stderr, "usage: %s <file> <iterations>\n", argv[0]);
		return 1;
	}

	const char *path = argv[1];
	long iters = atol(argv[2]);

	size_t len;
	char *data = read_file(path, &len);

	/* --- Benchmark: parse from buffer (fresh parser each time, non-zerocopy,
	 * since the buffer is reused across iterations and we want parity with
	 * the Go side which copies/owns its input via AddChunk on a []byte). --- */
	double start, end;
	long i;

	/* Warm up / correctness check */
	{
		struct ucl_parser *p = ucl_parser_new(0);
		if (!ucl_parser_add_chunk(p, (const unsigned char *)data, len)) {
			fprintf(stderr, "parse error on warmup: %s\n", ucl_parser_get_error(p));
			return 1;
		}
		ucl_object_t *obj = ucl_parser_get_object(p);
		if (!obj) {
			fprintf(stderr, "no object produced on warmup\n");
			return 1;
		}
		ucl_object_unref(obj);
		ucl_parser_free(p);
	}

	start = now_seconds();
	for (i = 0; i < iters; i++) {
		struct ucl_parser *p = ucl_parser_new(0);
		ucl_parser_add_chunk(p, (const unsigned char *)data, len);
		ucl_object_t *obj = ucl_parser_get_object(p);
		ucl_object_unref(obj);
		ucl_parser_free(p);
	}
	end = now_seconds();
	printf("parse\t%s\t%ld\t%.2f\n", path, iters, (end - start) * 1e9 / iters);

	/* Build one parsed object to reuse for emit benchmarks. */
	struct ucl_parser *p = ucl_parser_new(0);
	ucl_parser_add_chunk(p, (const unsigned char *)data, len);
	ucl_object_t *obj = ucl_parser_get_object(p);
	ucl_parser_free(p);

	/* --- Benchmark: emit as JSON --- */
	start = now_seconds();
	for (i = 0; i < iters; i++) {
		unsigned char *out = ucl_object_emit(obj, UCL_EMIT_JSON);
		free(out);
	}
	end = now_seconds();
	printf("emit_json\t%s\t%ld\t%.2f\n", path, iters, (end - start) * 1e9 / iters);

	/* --- Benchmark: emit as compact JSON --- */
	start = now_seconds();
	for (i = 0; i < iters; i++) {
		unsigned char *out = ucl_object_emit(obj, UCL_EMIT_JSON_COMPACT);
		free(out);
	}
	end = now_seconds();
	printf("emit_json_compact\t%s\t%ld\t%.2f\n", path, iters, (end - start) * 1e9 / iters);

	/* --- Benchmark: emit as UCL config --- */
	start = now_seconds();
	for (i = 0; i < iters; i++) {
		unsigned char *out = ucl_object_emit(obj, UCL_EMIT_CONFIG);
		free(out);
	}
	end = now_seconds();
	printf("emit_config\t%s\t%ld\t%.2f\n", path, iters, (end - start) * 1e9 / iters);

	ucl_object_unref(obj);
	free(data);

	return 0;
}
