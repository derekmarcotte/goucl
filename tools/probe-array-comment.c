#include "ucl.h"
#include <stdio.h>
#include <string.h>
static void dump(const char *label, const char *in) {
    struct ucl_parser *p = ucl_parser_new(0);
    ucl_parser_add_string(p, in, strlen(in));
    if (ucl_parser_get_error(p)) { printf("%-34s PARSE ERROR: %s\n", label, ucl_parser_get_error(p)); return; }
    const ucl_object_t *arr = ucl_object_lookup(ucl_parser_get_object(p), "arr");
    printf("%-34s ", label);
    ucl_object_iter_t it = NULL; const ucl_object_t *cur;
    while ((cur = ucl_object_iterate(arr, &it, true))) {
        const char *t = ucl_object_type_to_string(ucl_object_type(cur));
        if (ucl_object_type(cur) == UCL_STRING) printf("[%s \"%s\"] ", t, ucl_object_tostring(cur));
        else printf("[%s %ld] ", t, (long)ucl_object_toint(cur));
    }
    printf("\n");
}
int main(void) {
    dump("no comment, multiline",      "arr [\n  1,\n  2\n]\n");
    dump("no comment, one line",       "arr [ 1, 2 ]\n");
    dump("comment first, multiline",   "arr [\n  # c\n  1,\n  2\n]\n");
    dump("comment after first elem",   "arr [\n  1,\n  # c\n  2\n]\n");
    dump("block comment first",        "arr [\n  /* c */\n  1,\n  2\n]\n");
    dump("comment, no leading spaces", "arr [\n# c\n1,\n2\n]\n");
    dump("object: comment first",      "arr [\n  # c\n  {a=1},\n  2\n]\n");
    return 0;
}

/*
 * Build and run against the reference C library:
 *   cd ../libucl && gcc -I include -I src -o /tmp/probe \
 *       ../goucl/tools/probe-array-comment.c build/libucl.a -lm && /tmp/probe
 *
 * Demonstrates docs/findings.md #3: a comment on the first line inside an
 * array, followed by indented content, makes the next element lex as a string.
 */
