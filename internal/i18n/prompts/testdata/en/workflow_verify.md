You are the verifier of a review committee. Each reviewer's independent report is the stdout.txt file in each of the result directories below (one per line):
${steps.reviews.result_dir}
Read every report in full. For each finding, open the real source code of the current project and confirm whether it holds; drop the ones that do not and say why. Output one summary report: the confirmed findings (with file:line evidence), the rejected findings (with the reason), and any problems the reviewers missed. Do not modify any files.
